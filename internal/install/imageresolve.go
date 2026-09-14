package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Which server version an install gets.
//
// Naming a version in this plugin means the plugin has to be released whenever
// the server is, and it will not be: the pin sat eighteen patch releases behind
// within two weeks, so an evaluation installed a noticeably older server than
// the one being run in earnest. The version is therefore discovered rather than
// carried.
//
// What is deliberately *not* done is putting a floating tag in the Deployment.
// A manifest that says ":latest" installs whatever the kubelet happens to pull,
// which differs between the two nodes of one cluster and between two runs of
// one command, and leaves "what is running" unanswerable. So the newest version
// is resolved once, at install time, and the exact version it resolved to is
// what gets written into the manifest and kept.

// semverTag matches the only tags that name a release. The repository also
// carries "latest", per-architecture tags and build caches, none of which are a
// version anyone should be installed onto.
var semverTag = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// resolveLatestImage asks the registry which versions exist and returns the
// highest.
//
// Anonymous: the image is public, so the pull-scoped token the registry hands
// out without credentials is enough, and an evaluator needs no login before
// installing.
func resolveLatestImage(ctx context.Context) (string, error) {
	tags, err := listTags(ctx, DefaultImageRepo)
	if err != nil {
		return "", err
	}
	newest, err := highestSemver(tags)
	if err != nil {
		return "", err
	}
	return DefaultImageRepo + ":" + newest, nil
}

// highestSemver picks the greatest vMAJOR.MINOR.PATCH tag.
//
// Compared as numbers rather than as strings, because v0.4.9 sorts after
// v0.4.21 lexically and that is exactly the range these versions are in.
func highestSemver(tags []string) (string, error) {
	type version struct {
		parts [3]int
		tag   string
	}
	var versions []version
	for _, t := range tags {
		m := semverTag.FindStringSubmatch(t)
		if m == nil {
			continue
		}
		var v version
		v.tag = t
		for i := 0; i < 3; i++ {
			n, err := strconv.Atoi(m[i+1])
			if err != nil {
				continue
			}
			v.parts[i] = n
		}
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return "", fmt.Errorf("no released versions found in %s", DefaultImageRepo)
	}
	sort.Slice(versions, func(i, j int) bool {
		for k := 0; k < 3; k++ {
			if versions[i].parts[k] != versions[j].parts[k] {
				return versions[i].parts[k] < versions[j].parts[k]
			}
		}
		return false
	})
	return versions[len(versions)-1].tag, nil
}

// listTags reads a repository's tags from an OCI registry.
func listTags(ctx context.Context, image string) ([]string, error) {
	registry, repo, ok := strings.Cut(image, "/")
	if !ok {
		return nil, fmt.Errorf("cannot read a registry from %q", image)
	}

	token, err := pullToken(ctx, registry, repo)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("https://%s/v2/%s/tags/list?n=1000", registry, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing tags in %s: %w", image, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing tags in %s: %s", image, resp.Status)
	}

	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading the tag list for %s: %w", image, err)
	}
	return body.Tags, nil
}

// pullToken fetches the anonymous pull token a public repository issues.
func pullToken(ctx context.Context, registry, repo string) (string, error) {
	url := fmt.Sprintf("https://%s/token?scope=repository:%s:pull&service=%s", registry, repo, registry)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("getting a pull token for %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getting a pull token for %s: %s", repo, resp.Status)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("the registry returned no pull token for %s", repo)
	}
	return body.Token, nil
}

// resolveImage decides which server image this install uses, and says so.
//
// The order is the point:
//
//  1. --image wins. An explicit choice is never second-guessed, and it is how a
//     mirror, a local build, or a deliberate older version is named.
//  2. Otherwise an image this instance is already on wins. Re-running an install
//     resumes it; it does not upgrade the server underneath someone who only
//     wanted to re-apply their configuration.
//  3. Otherwise the newest released version, resolved from the registry now.
//  4. And if the registry cannot be reached, the version this plugin was built
//     with. Losing the network should cost an evaluator a slightly older server,
//     not the install.
func resolveImage(u UI, o *Options, priorImage string) error {
	switch {
	case o.Image != "":
		return nil

	case priorImage != "":
		o.Image = priorImage
		u.detail("keeping the image this instance is on (%s)", priorImage)
		u.detail("pass --image to move it")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	latest, err := resolveLatestImage(ctx)
	if err != nil {
		o.Image = defaultImage()
		u.warn("could not ask the registry for the newest version: %v", err)
		u.warn("falling back to %s, which this plugin was built with", o.Image)
		return nil
	}
	o.Image = latest
	return nil
}
