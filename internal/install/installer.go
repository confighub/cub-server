package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/confighub/sdk/core/cubapi"
	coreplugin "github.com/confighub/sdk/core/plugin"

	"github.com/confighub/cub-server/internal/config"
)

// Rendering with the installer.
//
// With --installer, the manifests come from ConfigHub's installer package,
// pulled from the registry the server image comes from and rendered by the
// installer, instead of from this plugin's own manifests. Everything around the
// render stays this plugin's: it still resolves the images and ports, generates
// the keys, the passwords and the Secrets -- which the package leaves out -- and
// applies, waits and signs in. What the installer renders is passed to it as the
// package's inputs, so the two renders describe the same instance.

// installerDir is the installer's work directory. Its presence is how a later
// command knows the instance was rendered with the installer.
func installerDir(outDir string) string {
	return filepath.Join(outDir, "installer")
}

// renderedWithInstaller reports whether the instance in outDir was last
// rendered with the installer.
func renderedWithInstaller(outDir string) bool {
	_, err := os.Stat(installerDir(outDir))
	return err == nil
}

// secretSyncWave puts the Secrets, which the package leaves out, in the
// Namespace's sync wave: before Postgres and Keycloak, which read them as they
// start.
const secretSyncWave = "-2"

// minimumInstallerVersion is the first installer that takes a JSON --input whole
// and applies --set-image to a component's images, both of which this relies on.
var minimumInstallerVersion = [3]int{0, 3, 4}

const (
	// installerCommand is the cub plugin command that runs the installer, as
	// `cub installer`.
	installerCommand = "installer"
	// installerInstall is how to get it.
	installerInstall = "cub plugin install confighub/installer"
	// installerUpgrade is how to get a newer one.
	installerUpgrade = "cub plugin upgrade installer"
)

// installerBinary finds the installer and returns its path and version: the
// entrypoint of the cub plugin that provides `cub installer`, or $INSTALLER,
// which names a build of it directly.
//
// The plugin's entrypoint is run rather than `cub installer`, because cub
// hands a plugin the caller's context, and a render has to stay away from it
// (see renderWithInstaller). An installer found on PATH is never used: on macOS
// that is Apple's package installer.
func installerBinary(ctx context.Context) (string, string, error) {
	name := os.Getenv("INSTALLER")
	if name == "" {
		path, err := installerPlugin()
		if err != nil {
			return "", "", err
		}
		name = path
	}
	out, err := run(ctx, name, "version")
	version, ok := strings.CutPrefix(strings.TrimSpace(out), "installer ")
	if err != nil || !ok {
		return "", "", fmt.Errorf("%s is not ConfigHub's installer.\n"+
			"    Install it with: %s", name, installerInstall)
	}
	// A build from source reports "dev", and is taken to be current.
	if parsed, ok := parseInstallerVersion(version); ok && olderThan(parsed, minimumInstallerVersion) {
		return "", "", fmt.Errorf("%s is installer %s; --installer needs %d.%d.%d or later.\n"+
			"    Upgrade it with: %s",
			name, version, minimumInstallerVersion[0], minimumInstallerVersion[1], minimumInstallerVersion[2], installerUpgrade)
	}
	return name, version, nil
}

// installerPlugin finds the entrypoint of the installed cub plugin that provides
// `cub installer`, the way cub does: in the plugins directory of the cub
// configuration, in plugin name order.
func installerPlugin() (string, error) {
	configDir, err := cubapi.DefaultConfigDir()
	if err != nil {
		return "", err
	}
	plugins := filepath.Join(configDir, "plugins")
	entries, _ := os.ReadDir(plugins)
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(plugins, entry.Name())
		m, err := coreplugin.Read(dir)
		if err != nil || m == nil {
			continue
		}
		for _, c := range m.Commands {
			if c.Name != installerCommand && !slices.Contains(c.Aliases, installerCommand) {
				continue
			}
			entrypoint := c.Entrypoint
			if entrypoint == "" {
				entrypoint = "main"
			}
			return filepath.Join(dir, entrypoint), nil
		}
	}
	return "", fmt.Errorf("--installer needs ConfigHub's installer, which is not installed as a cub plugin.\n"+
		"    Install it with: %s", installerInstall)
}

// installerPackage is the package to render: --installer-package when given,
// otherwise the newest version of ConfigHub's package that is not newer than
// the server, from the registry the server image comes from (ghcr.io for gcr.io,
// which has no copy of the package). The package's
// versions are the server's releases, so that is the package for this server,
// or, for a release the package did not change in, the one before it.
func installerPackage(ctx context.Context, o *Options, image string) (string, error) {
	if o.InstallerPackage != "" {
		return o.InstallerPackage, nil
	}
	repo := o.registryOf(image).Package
	tags, err := listTags(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("could not ask the registry which installer packages there are: %w\n"+
			"    Name one instead: --installer-package oci://%s:<version>", err, repo)
	}
	tag := packageVersionFor(tags, image)
	if tag == "" {
		return "", fmt.Errorf("%s has no installer package for %s.\n"+
			"    Name one instead: --installer-package oci://%s:<version>", repo, image, repo)
	}
	return "oci://" + repo + ":" + tag, nil
}

// packageVersionFor picks, from a package repository's tags, the newest version
// not newer than the server image's, or the newest of all for an image whose
// tag is not a version. Empty when there is none.
func packageVersionFor(tags []string, image string) string {
	server, _, known := serverVersion(image)
	var best [3]int
	bestTag := ""
	for _, tag := range tags {
		v, ok := parseInstallerVersion(tag)
		if !ok || (known && olderThan(server, v)) {
			continue
		}
		if bestTag == "" || olderThan(best, v) {
			best, bestTag = v, tag
		}
	}
	return bestTag
}

// parseInstallerVersion reads a version like 0.3.4 or v0.3.4.
func parseInstallerVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// validateForInstaller refuses what the package cannot express: it has no input
// for a Keycloak realm, organization or client other than the defaults, which
// the plugin's own manifests can set.
func validateForInstaller(opts config.Options) error {
	k := opts.Keycloak
	if k == nil {
		return nil
	}
	for _, c := range []struct{ flag, got, want string }{
		{"--realm", k.Realm, config.DefaultKeycloakRealm},
		{"--org", k.Org, config.DefaultKeycloakOrg},
		{"--client-id", k.ClientID, config.DefaultKeycloakClientID},
		{"device client", k.DeviceClientID, config.DefaultKeycloakDeviceClientID},
		{"UI client", k.UIClientID, config.DefaultKeycloakUIClientID},
		{"admin user", k.AdminUser, config.DefaultKeycloakAdminUser},
	} {
		if c.got != c.want {
			return fmt.Errorf("--installer renders Keycloak's %s as %q, and this instance asks for %q; drop --installer to render it with this plugin's manifests",
				c.flag, c.want, c.got)
		}
	}
	return nil
}

// installerArgs is the `installer setup` invocation that renders this instance:
// the components its options select and the package's inputs from its resolved
// configuration.
func installerArgs(opts config.Options, s *config.Surface, pkg, workDir string) ([]string, error) {
	args := []string{"setup", "--pull", pkg, "--work-dir", workDir, "--non-interactive", "--namespace", opts.Namespace}

	var selected []string
	if opts.Database == config.DatabaseInternal {
		selected = append(selected, "bundled-postgres")
	}
	if opts.Keycloak != nil {
		selected = append(selected, "keycloak")
	}
	if opts.APINodePort != 0 || opts.OCINodePort != 0 || opts.UINodePort != 0 {
		selected = append(selected, "nodeport")
	}
	if opts.Ingress == config.IngressTraefik {
		selected = append(selected, "traefik")
	}
	if len(selected) == 0 {
		args = append(args, "--components", "minimal")
	}
	for _, c := range selected {
		args = append(args, "--select", c)
	}

	input := func(name, value string) {
		if value != "" {
			args = append(args, "--input", name+"="+value)
		}
	}
	port := func(name string, p int) {
		if p != 0 {
			input(name, strconv.Itoa(p))
		}
	}
	input("api_url", opts.APIURL)
	input("ui_url", opts.UIURL)
	// From the surface rather than opts, so a URL kept from a previous run is
	// passed as well as one given now.
	input("oci_url", s.Get(config.OCIURLEnv))
	input("admin_jwk", s.Get(config.AdminJWKEnv))
	port("api_node_port", opts.APINodePort)
	port("oci_node_port", opts.OCINodePort)
	port("ui_node_port", opts.UINodePort)
	if opts.Ingress == config.IngressTraefik {
		input("host", opts.Host)
	}
	// Only when they differ from the package's defaults, so an install that
	// names neither renders with a package from before these inputs existed.
	if opts.Database == config.DatabaseInternal {
		input("storage_class", opts.StorageClass)
		if opts.StorageSize != config.DefaultStorageSize {
			input("storage_size", opts.StorageSize)
		}
	}

	image := func(name, ref string) {
		if ref != "" {
			args = append(args, "--set-image", name+"="+ref)
		}
	}
	image("ghcr.io/confighubai/confighub", opts.Image)
	image("ghcr.io/confighub/ui", opts.UIImage)
	if opts.Database == config.DatabaseInternal {
		image("postgres", opts.DatabaseImage)
	}

	if k := opts.Keycloak; k != nil {
		// The public half of the key the server signs its assertions with, which
		// Keycloak imports with the realm.
		jwks, err := config.ClientJWKS(s.Get(config.KeycloakClientKeyEnv))
		if err != nil {
			return nil, err
		}
		input("keycloak_url", strings.TrimSuffix(k.PublicURL, "/"))
		input("keycloak_client_jwks", jwks)
		port("keycloak_node_port", k.NodePort)
		input("keycloak_org_domain", k.OrgDomain)
		input("keycloak_ssl_required", k.SSLRequired)
		image("quay.io/keycloak/keycloak", k.Image)
	}
	return args, nil
}

// renderWithInstaller renders the instance's manifests with the installer, and
// returns them as files of its config directory. The Secrets are rendered by
// the caller.
func renderWithInstaller(ctx context.Context, u UI, o *Options, opts config.Options, s *config.Surface) ([]config.File, error) {
	bin, version, err := installerBinary(ctx)
	if err != nil {
		return nil, err
	}
	pkg, err := installerPackage(ctx, o, opts.Image)
	if err != nil {
		return nil, err
	}
	work := installerDir(o.OutDir)
	args, err := installerArgs(opts, s, pkg, work)
	if err != nil {
		return nil, err
	}

	// An empty cub configuration, so that rendering never reaches whichever
	// ConfigHub cub is pointed at: the installer otherwise looks there for an
	// earlier install of the package when its work directory has none.
	cubConfig, err := os.MkdirTemp("", "cub-server-installer-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(cubConfig)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(cubEnv(), "CUB_CONFIG="+cubConfig)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("installer setup: %w\n%s", err, strings.TrimSpace(buf.String()))
	}

	paths, err := filepath.Glob(filepath.Join(work, "out", "manifests", "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	files := make([]config.File, 0, len(paths))
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files = append(files, config.File{Path: config.ConfigDir + "/" + filepath.Base(p), Content: content})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("installer setup rendered nothing into %s", filepath.Join(work, "out", "manifests"))
	}
	u.detail("rendered %d manifests from %s with installer %s", len(files), pkg, version)
	return files, nil
}

// configDigests digests the configuration each Deployment reads, from the
// render in outDir: the server's ConfigMap and Secret, and the UI's ConfigMap.
// A Deployment whose configuration the render does not hold is left out.
//
// The installer's Deployments carry no hash of their configuration, as this
// plugin's do, so a changed ConfigMap leaves their pod templates as they were and
// an apply restarts nothing. Comparing these before and after a render is how an
// install knows which Deployments to restart.
func configDigests(outDir string) map[string]string {
	digests := map[string]string{}
	server, _ := renderedConfigMapData(outDir, serverConfigMap)
	if server != nil {
		secret, _ := readStringData(filepath.Join(outDir, config.SecretsDir, config.SecretFile))
		digests[serverDeploymentName] = digest(server, secret)
	}
	if ui, _ := renderedConfigMapData(outDir, uiConfigMap); ui != nil {
		digests[uiDeploymentName] = digest(ui)
	}
	return digests
}

func digest(maps ...map[string]string) string {
	h := sha256.New()
	for _, m := range maps {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%d:%s=%d:%s\n", len(k), k, len(m[k]), m[k])
		}
		fmt.Fprintln(h, "--")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// restartChanged restarts each Deployment whose configuration changed between
// two renders. One that had none before is new, and starts on its
// configuration already.
func restartChanged(ctx context.Context, u UI, o *Options, kube kubeEnv, before, after map[string]string) error {
	for _, name := range []string{serverDeploymentName, uiDeploymentName} {
		if before[name] == "" || before[name] == after[name] {
			continue
		}
		u.detail("%s: its configuration changed, so restarting it", name)
		if _, err := run(ctx, toolKubectl.name, kube.args("-n", o.Namespace, "rollout", "restart", "deployment/"+name)...); err != nil {
			return err
		}
	}
	return nil
}

// writeRendered writes a render to outDir, first clearing the config directory,
// so that a manifest an earlier render wrote and this one did not -- a Keycloak
// removed, the other renderer's file names -- is not left looking current.
// The Secrets directory is kept: its values are what a re-run preserves.
func writeRendered(outDir string, files []config.File) error {
	stale, err := filepath.Glob(filepath.Join(outDir, config.ConfigDir, "*.yaml"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	for _, f := range files {
		path := filepath.Join(outDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.Sensitive {
			mode = 0o600
		}
		if err := writeFile(path, f.Content, mode); err != nil {
			return err
		}
	}
	return nil
}

// render produces an instance's files, Secrets included: with the installer, or
// with this plugin's manifests.
func render(ctx context.Context, u UI, o *Options, opts config.Options, s *config.Surface) ([]config.File, error) {
	if !o.Installer {
		files, err := config.Render(s, opts)
		if err != nil {
			return nil, err
		}
		// Rendered with this plugin's manifests now, so the next command should
		// not take the instance for one rendered with the installer.
		if err := os.RemoveAll(installerDir(o.OutDir)); err != nil {
			return nil, err
		}
		return files, nil
	}
	files, err := renderWithInstaller(ctx, u, o, opts, s)
	if err != nil {
		return nil, err
	}
	secrets, err := config.RenderSecrets(s, opts, secretSyncWave)
	if err != nil {
		return nil, err
	}
	return append(files, secrets...), nil
}
