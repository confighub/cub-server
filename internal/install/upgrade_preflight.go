package install

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// What an upgrade finds out before it changes anything.
//
// The server's Deployment is replaced, not rolled: the old pod is gone before
// the new one pulls its image and migrates the database. An image that is not
// in the registry the instance pulls from, or a migration nobody can undo, is
// therefore found out with the instance down. These checks move that moment to
// before the first patch.

// releaseRegistries are where the newest release is looked up, in order. Both
// carry every release; a network that reaches only one of them still gets an
// answer.
var releaseRegistries = []Registry{GHCR, GCR}

// listReleaseTags is listTags, replaceable in tests.
var listReleaseTags = listTags

// newestRelease is the tag of the newest ConfigHub release, asked of each
// release registry in turn. The first that answers decides.
func newestRelease(ctx context.Context) (string, error) {
	var failures []string
	for _, r := range releaseRegistries {
		lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tags, err := listReleaseTags(lookupCtx, r.Server)
		cancel()
		if err == nil {
			var tag string
			if tag, err = highestSemver(tags); err == nil {
				return tag, nil
			}
		}
		failures = append(failures, fmt.Sprintf("%s: %v", r.Server, err))
	}
	return "", fmt.Errorf("could not ask a registry which ConfigHub version is newest:\n      %s\n"+
		"    Name one instead: cub server upgrade --version <version>", strings.Join(failures, "\n      "))
}

// imagePresence is what one look at a registry said about an image.
type imagePresence int

const (
	imageUnknown imagePresence = iota // the registry did not say, to this machine
	imageFound
	imageMissing
)

// imageCheck is one target image and whether its registry has it.
type imageCheck struct {
	image    string
	presence imagePresence
	why      string // for imageUnknown: what stopped the check
}

// checkImage asks an image's registry whether it holds the image.
//
// Anonymously, as a tag lookup does. A public registry answers yes or no. A
// private mirror usually answers neither without credentials this command does
// not have, and that is reported as unknown, never as missing: only a registry
// that was asked and said 404 is taken at its word.
func checkImage(ctx context.Context, client *http.Client, image string) imageCheck {
	c := imageCheck{image: image}
	registry, rest, ok := strings.Cut(image, "/")
	if !ok || !strings.ContainsAny(registry, ".:") && registry != "localhost" {
		c.why = "it names no registry"
		return c
	}
	repo, tag := rest, "latest"
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "/") {
		repo, tag = rest[:i], rest[i+1:]
	}
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	resp, err := headManifest(ctx, client, url, "")
	if err != nil {
		c.why = "the registry did not answer from this machine"
		return c
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := pullToken(ctx, resp.Header.Get("WWW-Authenticate"), repo)
		if err != nil {
			c.why = "the registry wants credentials"
			return c
		}
		if resp, err = headManifest(ctx, client, url, token); err != nil {
			c.why = "the registry did not answer from this machine"
			return c
		}
		resp.Body.Close()
	}
	switch resp.StatusCode {
	case http.StatusOK:
		c.presence = imageFound
	case http.StatusNotFound:
		c.presence = imageMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		c.why = "the registry wants credentials"
	default:
		c.why = "the registry answered " + resp.Status
	}
	return c
}

func headManifest(ctx context.Context, client *http.Client, url, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return nil, err
	}
	// A release is a multi-architecture index; a registry asked for a plain
	// manifest only answers 404 for one.
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}

// checkImages looks for each image the upgrade moves to.
func checkImages(ctx context.Context, images []string) []imageCheck {
	client := &http.Client{Timeout: 15 * time.Second}
	checks := make([]imageCheck, 0, len(images))
	for _, image := range images {
		checks = append(checks, checkImage(ctx, client, image))
	}
	return checks
}

// imageCheckLines reports the checks, and returns the images a registry said it
// does not have.
func imageCheckLines(checks []imageCheck) (lines, missing []string) {
	unknown := false
	lines = append(lines, "", "The images the new version pulls:", "")
	for _, c := range checks {
		switch c.presence {
		case imageFound:
			lines = append(lines, "  found      "+c.image)
		case imageMissing:
			lines = append(lines, "  NOT FOUND  "+c.image)
			missing = append(missing, c.image)
		default:
			unknown = true
			lines = append(lines, "  unchecked  "+c.image+"  ("+c.why+")")
		}
	}
	if len(missing) > 0 {
		lines = append(lines, "", "Copy the missing images into that registry before upgrading.")
	}
	if unknown {
		lines = append(lines, "",
			"Make sure the unchecked images are in the registry before upgrading: the server",
			"is stopped before its new image is pulled, and stays down if the pull fails.")
	}
	return lines, missing
}

// hasBundledDatabase reports whether the instance runs the Postgres an install
// bundles, by the name the manifests give it.
func hasBundledDatabase(ctx context.Context, kube kubeEnv, namespace string) bool {
	_, err := run(ctx, toolKubectl.name, kube.args("-n", namespace, "get", "statefulset", databaseStatefulSet, "-o", "name")...)
	return err == nil
}

// backupLines says how to take a backup first. The new version migrates the
// database as it starts, an older server cannot read a migrated database, and
// this command does not downgrade, so a backup is the only way back.
func backupLines(kube kubeEnv, namespace, from string, bundled bool) []string {
	lines := []string{"",
		"The new version migrates the database when it starts, and " + from + " cannot run on a",
		"migrated database. A backup taken first is the only way back:",
		""}
	if !bundled {
		return append(lines, "  back up the database the server's DATABASE_URL names, with whatever runs it")
	}
	args := kube.args("-n", namespace, "exec", "statefulset/"+databaseStatefulSet, "--",
		"pg_dump", "-U", "confighub", "-Fc", "confighub")
	return append(lines, "  kubectl "+strings.Join(args, " ")+" > confighub-"+from+".dump")
}

// minorChangeLines names what else has to move when the upgrade crosses a
// minor version. Before 1.0 a change in the second number is not backward
// compatible, and only cub checks for it.
func minorChangeLines(from, to [3]int) []string {
	if from[0] == to[0] && from[1] == to[1] {
		return nil
	}
	return []string{"",
		fmt.Sprintf("This moves the API from %d.%d to %d.%d. Before 1.0 that is not backward compatible,",
			from[0], from[1], to[0], to[1]),
		"so move what talks to the server along with it:",
		"",
		"  cub                 cub upgrade, on every machine that uses this instance",
		"  k8sbot or argobot   the image on each attached cluster, to a release built for the new API",
		"  your own clients    anything that calls the API or uses the SDK",
	}
}
