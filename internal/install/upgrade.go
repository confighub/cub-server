package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Upgrading an instance that is already running.
//
// The instance is read from the cluster rather than from a record of how it was
// installed, because the record may not exist: an instance deployed from
// manifests someone edited and applied has none, and one installed here may
// have been changed since. What is running is the only description of it that
// is sure to be true.
//
// An upgrade here is a new version of the server and the UI. It changes the tag
// of each image and nothing else: the registry an image comes from is the
// operator's choice -- a mirror, often -- and is kept. Database migrations need
// no step of their own: the server's init container runs them on start.
//
// The change is written as patches before anything is applied, because an
// operator who keeps manifests applies them from those manifests, and a change
// made only to the live objects is undone by their next apply. --apply applies
// the same patches.

// UpgradeOptions is what `cub server upgrade` needs.
type UpgradeOptions struct {
	// Kubeconfig, KubeContext and Namespace locate the instance. Empty
	// Kubeconfig and KubeContext mean kubectl's defaults.
	Kubeconfig  string
	KubeContext string
	Namespace   string

	// Version is the release to move to. Empty means the newest one published.
	Version string

	// OutDir is where the patches are written.
	OutDir string

	// Apply applies the patches as well as writing them.
	Apply bool
}

// The Deployments an install consists of, as the manifests name them.
const serverDeploymentName = "confighub"

// deploymentImages is what an upgrade needs to know about one Deployment: each
// container's image, by name, init containers included.
type deploymentImages struct {
	name           string
	containers     map[string]string
	initContainers map[string]string
}

// upgradePlan is the change to make: one patch per Deployment, and a line per
// image for the operator to read.
type upgradePlan struct {
	from, to string
	patches  []deploymentPatch
	changes  []string
}

type deploymentPatch struct {
	deployment string
	body       []byte
}

// RunUpgrade moves a running instance to a newer release.
func RunUpgrade(ctx context.Context, u UI, o *UpgradeOptions) error {
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	kube := kubeEnv{kubeconfig: o.Kubeconfig, context: o.KubeContext}

	u.step("Reading the instance")
	server, err := readDeploymentImages(ctx, kube, o.Namespace, serverDeploymentName)
	if err != nil {
		return err
	}
	ui, err := readDeploymentImages(ctx, kube, o.Namespace, uiDeploymentName)
	if err != nil {
		return err
	}
	serverImage, ok := server.containers[serverDeploymentName]
	if !ok {
		return fmt.Errorf("deployment %s/%s has no container named %s", o.Namespace, serverDeploymentName, serverDeploymentName)
	}
	current, currentTag, ok := serverVersion(serverImage)
	if !ok {
		return fmt.Errorf("the server runs %s, which is not a released version; pass the image you want to the deployment yourself", serverImage)
	}
	u.detail("server %s", serverImage)
	u.detail("UI     %s", strings.Join(sortedValues(ui.containers), ", "))

	target := o.Version
	if target == "" {
		resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		latest, err := resolveLatestImage(resolveCtx, GHCR.Server)
		cancel()
		if err != nil {
			return fmt.Errorf("could not ask the registry which ConfigHub version is newest: %w\n"+
				"    Name one instead: cub server upgrade --version <version>", err)
		}
		target = latest[strings.LastIndex(latest, ":")+1:]
	}
	if !strings.HasPrefix(target, "v") {
		target = "v" + target
	}
	targetVersion, _, ok := serverVersion("x:" + target)
	if !ok {
		return fmt.Errorf("--version %q is not a release version like v0.9.5", o.Version)
	}
	if olderThan(targetVersion, minimumServerVersion) {
		return fmt.Errorf("%s is older than v%d.%d.%d, the oldest server this plugin supports", target,
			minimumServerVersion[0], minimumServerVersion[1], minimumServerVersion[2])
	}
	if olderThan(targetVersion, current) {
		return fmt.Errorf("the server is on %s, newer than %s; this command does not downgrade", currentTag, target)
	}

	plan := planUpgrade(o.Namespace, server, ui, serverImage, currentTag, target)
	if len(plan.patches) == 0 {
		u.section("Already on "+target+".", "The server and the UI both run it; nothing to change.")
		return nil
	}

	if o.OutDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		where := o.KubeContext
		if where == "" {
			where = "current-context"
		}
		o.OutDir = filepath.Join(home, ".confighub", "upgrades", sanitize(where)+"-"+sanitize(o.Namespace), target)
	}
	paths, err := writeUpgradePatches(o.OutDir, plan)
	if err != nil {
		return err
	}

	if !o.Apply {
		return reportUpgradePlan(u, o, kube, plan, paths)
	}

	// The server first: its init container migrates the database, and a UI
	// newer than the API it talks to is the combination never tested.
	u.step("Upgrading to %s", target)
	for i, p := range plan.patches {
		if _, err := run(ctx, toolKubectl.name, kube.args(
			"-n", o.Namespace, "patch", "deployment", p.deployment, "--type", "strategic", "--patch-file", paths[i],
		)...); err != nil {
			return err
		}
		u.detail("%s: waiting for the rollout", p.deployment)
		if err := kube.waitForRollout(ctx, o.Namespace, "deployment", p.deployment, rolloutTimeout); err != nil {
			return err
		}
	}

	lines := append([]string{}, plan.changes...)
	if api := advertisedAPIURL(ctx, kube, o.Namespace); api != "" {
		if got, err := reportedVersion(ctx, api); err == nil {
			lines = append(lines, "", "The API at "+api+" reports "+got+".")
		} else {
			lines = append(lines, "", "Could not ask the API at "+api+" for its version: "+err.Error())
		}
	}
	lines = append(lines, "",
		"The live Deployments now differ from any manifests they were applied from.",
		"Merge the same patches into those, so the next apply keeps this version:",
		"")
	for _, p := range paths {
		lines = append(lines, "  "+p)
	}
	u.section("Upgraded to "+target+".", lines...)
	return nil
}

// readDeploymentImages reads one Deployment's container images.
func readDeploymentImages(ctx context.Context, kube kubeEnv, namespace, name string) (*deploymentImages, error) {
	out, err := run(ctx, toolKubectl.name, kube.args("-n", namespace, "get", "deployment", name, "-o", "json")...)
	if err != nil {
		return nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, name, err)
	}
	var d struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers     []struct{ Name, Image string } `json:"containers"`
					InitContainers []struct{ Name, Image string } `json:"initContainers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, name, err)
	}
	images := &deploymentImages{name: name, containers: map[string]string{}, initContainers: map[string]string{}}
	for _, c := range d.Spec.Template.Spec.Containers {
		images.containers[c.Name] = c.Image
	}
	for _, c := range d.Spec.Template.Spec.InitContainers {
		images.initContainers[c.Name] = c.Image
	}
	return images, nil
}

// planUpgrade works out the patches that move the instance to target.
//
// In the server's Deployment, every container running the server's image is
// moved -- that is the server and the init container that migrates its
// database -- and anything else (a sidecar someone added) is left alone. In the
// UI's, every container is: the UI image is released under the server's
// version, so the tag is the same.
func planUpgrade(namespace string, server, ui *deploymentImages, serverImage, from, target string) upgradePlan {
	plan := upgradePlan{from: from, to: target}
	serverRepo := imageRepo(serverImage)

	type change struct{ containers, initContainers map[string]string }
	for _, d := range []struct {
		images  *deploymentImages
		matches func(image string) bool
	}{
		{server, func(image string) bool { return imageRepo(image) == serverRepo }},
		{ui, func(string) bool { return true }},
	} {
		c := change{containers: map[string]string{}, initContainers: map[string]string{}}
		for _, set := range []struct {
			from map[string]string
			to   map[string]string
			kind string
		}{
			{d.images.initContainers, c.initContainers, "init container"},
			{d.images.containers, c.containers, "container"},
		} {
			for _, name := range sortedKeys(set.from) {
				image := set.from[name]
				if !d.matches(image) {
					continue
				}
				next := imageRepo(image) + ":" + target
				if next == image {
					continue
				}
				set.to[name] = next
				plan.changes = append(plan.changes, fmt.Sprintf("%s %s %s: %s -> %s", d.images.name, set.kind, name, image, next))
			}
		}
		if len(c.containers) == 0 && len(c.initContainers) == 0 {
			continue
		}
		plan.patches = append(plan.patches, deploymentPatch{
			deployment: d.images.name,
			body:       deploymentImagePatch(namespace, d.images.name, c.containers, c.initContainers),
		})
	}
	return plan
}

// deploymentImagePatch is a strategic merge patch setting container images.
//
// Strategic, because it merges containers by name: the patch names only the
// image of each container it changes and leaves everything else in the
// Deployment as it is. With its apiVersion, kind, name and namespace it is also
// a patch kustomize accepts as it is.
func deploymentImagePatch(namespace, name string, containers, initContainers map[string]string) []byte {
	var b strings.Builder
	b.WriteString("# Written by `cub server upgrade`. Merge into the Deployment's manifest, or:\n")
	b.WriteString("#   kubectl patch deployment " + name + " --type strategic --patch-file <this file>\n")
	// The namespace too: kustomize matches a patch to a resource by it, and the
	// generated manifests name theirs.
	b.WriteString("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: " + name + "\n  namespace: " + namespace + "\n")
	b.WriteString("spec:\n  template:\n    spec:\n")
	for _, set := range []struct {
		key    string
		images map[string]string
	}{{"initContainers", initContainers}, {"containers", containers}} {
		if len(set.images) == 0 {
			continue
		}
		b.WriteString("      " + set.key + ":\n")
		for _, n := range sortedKeys(set.images) {
			fmt.Fprintf(&b, "      - name: %s\n        image: %s\n", n, set.images[n])
		}
	}
	return []byte(b.String())
}

func writeUpgradePatches(dir string, plan upgradePlan) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range plan.patches {
		path := filepath.Join(dir, p.deployment+".deployment.patch.yaml")
		if err := writeFile(path, p.body, 0o644); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func reportUpgradePlan(u UI, o *UpgradeOptions, kube kubeEnv, plan upgradePlan, paths []string) error {
	lines := append([]string{}, plan.changes...)
	lines = append(lines, "",
		"Nothing was changed. Merge these patches into the manifests you apply,",
		"then apply those:",
		"")
	for _, p := range paths {
		lines = append(lines, "  "+p)
	}
	lines = append(lines, "", "or patch what is running, server first:", "")
	for i, p := range plan.patches {
		args := kube.args("-n", o.Namespace, "patch", "deployment", p.deployment, "--type", "strategic", "--patch-file", paths[i])
		lines = append(lines, "  kubectl "+strings.Join(args, " "))
	}
	lines = append(lines, "", "or have this command do it: re-run with --apply.")
	u.section("Upgrade from "+plan.from+" to "+plan.to+".", lines...)
	return nil
}

// advertisedAPIURL is where the browser reaches the API, read from the UI's
// configuration, which is the one place an instance records it. Empty when it
// cannot be read.
func advertisedAPIURL(ctx context.Context, kube kubeEnv, namespace string) string {
	out, err := run(ctx, toolKubectl.name, kube.args(
		"-n", namespace, "get", "configmap", "confighub-ui-env", "-o", "jsonpath={.data.CONFIGHUB_URL}",
	)...)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(out), "/")
}

// reportedVersion asks the API which version it is.
func reportedVersion(ctx context.Context, api string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/api/info", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct{ Version string }
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.Version, nil
}

// imageRepo is an image reference without its tag or digest. A colon after the
// last slash is a tag; one before it is a registry port.
func imageRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i]
	}
	return image
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedValues(m map[string]string) []string {
	values := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		values = append(values, m[k])
	}
	return values
}
