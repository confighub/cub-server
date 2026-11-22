package install

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// installerOptions are the deployment options of a local install, as Run would
// pass them.
func installerOptions(t *testing.T, keycloak bool) (config.Options, *config.Surface) {
	t.Helper()
	opts := config.Options{
		Namespace:   "confighub",
		Image:       "ghcr.io/confighubai/confighub:v0.10.3",
		UIImage:     "ghcr.io/confighub/ui:v0.10.3",
		APIURL:      "http://localhost:32180",
		UIURL:       "http://localhost:32183",
		APINodePort: 32180, OCINodePort: 32181, UINodePort: 32183,
	}
	if keycloak {
		opts.Keycloak = &config.Keycloak{PublicURL: "http://localhost:32182/", UIRedirectURI: "http://localhost:32183/", NodePort: 32182}
	}
	opts.Defaults()
	s, err := config.Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	return opts, s
}

// A local install selects the bundled database and NodePorts, and passes the
// instance's addresses, ports, administrator key and images as the package's
// inputs.
func TestInstallerSetup(t *testing.T) {
	opts, s := installerOptions(t, false)
	setup, err := installerSetup(opts, s, "oci://ghcr.io/confighub/packages/confighub:0.10.3", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if setup.Pull != "oci://ghcr.io/confighub/packages/confighub:0.10.3" || setup.WorkDir != "/work" || setup.Namespace != "confighub" || !setup.Render {
		t.Errorf("pulls %q into %q, namespace %q, render %v", setup.Pull, setup.WorkDir, setup.Namespace, setup.Render)
	}
	if !slices.Equal(setup.Select, []string{"bundled-postgres", "nodeport"}) || setup.Preset != "" {
		t.Errorf("selects %v (preset %q), want bundled-postgres and nodeport", setup.Select, setup.Preset)
	}
	for name, want := range map[string]string{
		"api_url": "http://localhost:32180", "ui_url": "http://localhost:32183",
		"admin_jwk":     s.Get(config.AdminJWKEnv),
		"api_node_port": "32180", "oci_node_port": "32181", "ui_node_port": "32183",
	} {
		if got := setup.Inputs[name]; got != want {
			t.Errorf("input %s = %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"ghcr.io/confighubai/confighub": "ghcr.io/confighubai/confighub:v0.10.3",
		"ghcr.io/confighub/ui":          "ghcr.io/confighub/ui:v0.10.3",
		"postgres":                      config.DefaultDatabaseImage,
	} {
		if got := setup.SetImage[name]; got != want {
			t.Errorf("image %s set to %q, want %q", name, got, want)
		}
	}
	for name := range setup.Inputs {
		if strings.HasPrefix(name, "keycloak_") {
			t.Errorf("passed %s without Keycloak", name)
		}
	}
	// Unset, the package leaves OCI_EXTERNAL_URL empty and advertises the
	// NodePort.
	if got, ok := setup.Inputs["oci_url"]; ok {
		t.Errorf("passed oci_url %q with no registry URL", got)
	}
}

// An install with nothing to select renders the package's minimal preset.
func TestInstallerSetupMinimal(t *testing.T) {
	opts, s := installerOptions(t, false)
	opts.Database = config.DatabaseExternal
	opts.APINodePort, opts.OCINodePort, opts.UINodePort = 0, 0, 0
	setup, err := installerSetup(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if setup.Preset != "minimal" || len(setup.Select) != 0 {
		t.Errorf("preset %q, selects %v; want the minimal preset alone", setup.Preset, setup.Select)
	}
}

// The registry's URL reaches the package as oci_url, whether it was given now
// or kept from the previous run.
func TestInstallerSetupWithOCIURL(t *testing.T) {
	for name, prior := range map[string]config.Preserved{
		"given now":              nil,
		"kept from the last run": {config.OCIURLEnv: "https://confighub-oci.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			opts, _ := installerOptions(t, false)
			if prior == nil {
				opts.OCIURL = "https://confighub-oci.example.com"
			}
			s, err := config.Build(opts, prior)
			if err != nil {
				t.Fatal(err)
			}
			setup, err := installerSetup(opts, s, "/pkg", "/work")
			if err != nil {
				t.Fatal(err)
			}
			if got := setup.Inputs["oci_url"]; got != "https://confighub-oci.example.com" {
				t.Errorf("input oci_url = %q", got)
			}
		})
	}
}

// With Keycloak, the component is selected and given its address, without the
// trailing slash, and the public half of the server's client key.
func TestInstallerSetupWithKeycloak(t *testing.T) {
	opts, s := installerOptions(t, true)
	setup, err := installerSetup(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(setup.Select, "keycloak") {
		t.Errorf("did not select keycloak: %v", setup.Select)
	}
	jwks, err := config.ClientJWKS(s.Get(config.KeycloakClientKeyEnv))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"keycloak_url": "http://localhost:32182", "keycloak_client_jwks": jwks, "keycloak_node_port": "32182",
		"keycloak_org_domain": config.DefaultKeycloakOrgDomain, "keycloak_ssl_required": "none",
	} {
		if got := setup.Inputs[name]; got != want {
			t.Errorf("input %s = %q, want %q", name, got, want)
		}
	}
	if got := setup.SetImage["quay.io/keycloak/keycloak"]; got != config.DefaultKeycloakImage {
		t.Errorf("Keycloak's image set to %q", got)
	}
}

// The package names Keycloak's realm, organization and clients after the
// defaults, so asking for others has to be refused rather than ignored.
func TestValidateForInstaller(t *testing.T) {
	opts, _ := installerOptions(t, true)
	if err := validateForInstaller(opts); err != nil {
		t.Errorf("refused the defaults: %v", err)
	}
	opts.Keycloak.Realm = "acme"
	if err := validateForInstaller(opts); err == nil || !strings.Contains(err.Error(), "--realm") {
		t.Errorf("got %v, want a refusal naming --realm", err)
	}
}

// The package is the server's release, or the newest before it when the
// release did not change the package.
func TestPackageVersionFor(t *testing.T) {
	tags := []string{"0.10.2", "0.10.3", "0.10.10", "0.11.0", "latest", "sha256-abc.sig"}
	for image, want := range map[string]string{
		"ghcr.io/confighubai/confighub:v0.10.3":  "0.10.3",
		"ghcr.io/confighubai/confighub:v0.10.9":  "0.10.3",
		"ghcr.io/confighubai/confighub:v0.10.11": "0.10.10",
		"ghcr.io/confighubai/confighub:v0.10.1":  "",
		"registry.example.com/confighub:dev":     "0.11.0",
	} {
		if got := packageVersionFor(tags, image); got != want {
			t.Errorf("packageVersionFor(%s) = %q, want %q", image, got, want)
		}
	}
}

func TestParseInstallerVersion(t *testing.T) {
	for in, want := range map[string]bool{"0.3.4": true, "v0.3.4": true, "dev": false, "0.3": false} {
		if _, ok := parseInstallerVersion(in); ok != want {
			t.Errorf("parseInstallerVersion(%q) ok = %v, want %v", in, ok, want)
		}
	}
}

// A Deployment is restarted when the configuration it reads changed, and only
// then: the installer's pod templates carry no hash of it.
func TestConfigDigestsFollowTheConfiguration(t *testing.T) {
	dir := t.TempDir()
	opts, s := installerOptions(t, false)
	write := func(opts config.Options, s *config.Surface) map[string]string {
		files, err := config.Render(s, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeRendered(dir, files); err != nil {
			t.Fatal(err)
		}
		return configDigests(dir)
	}
	if got := configDigests(dir); len(got) != 0 {
		t.Fatalf("a first install has configuration to compare: %v", got)
	}
	first := write(opts, s)
	if first[serverDeploymentName] == "" || first[uiDeploymentName] == "" {
		t.Fatalf("missing a digest: %v", first)
	}
	if again := write(opts, s); again[serverDeploymentName] != first[serverDeploymentName] || again[uiDeploymentName] != first[uiDeploymentName] {
		t.Error("the same configuration digested differently")
	}

	opts.UIURL = "http://localhost:32190"
	opts.APIURL = "http://localhost:32191"
	changed, err := config.Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := write(opts, changed)
	if next[serverDeploymentName] == first[serverDeploymentName] {
		t.Error("the server's configuration changed, and its digest did not")
	}
	if next[uiDeploymentName] == first[uiDeploymentName] {
		t.Error("the UI's configuration changed, and its digest did not")
	}
}

// The installer, rendering this repository's package: the render reads back
// the way a plugin render does, so re-running and the other commands work on
// it.
func TestRenderWithInstaller(t *testing.T) {
	ctx := context.Background()
	pkg, err := filepath.Abs(filepath.Join("..", "config", "package"))
	if err != nil {
		t.Fatal(err)
	}
	opts, s := installerOptions(t, true)
	opts.Metrics = true
	o := &Options{OutDir: t.TempDir(), Installer: true, InstallerPackage: pkg}
	files, err := render(ctx, UI{Out: &strings.Builder{}}, o, opts, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRendered(o.OutDir, files); err != nil {
		t.Fatal(err)
	}
	if !renderedWithInstaller(o.OutDir) {
		t.Error("the instance does not read back as rendered with the installer")
	}

	prior, adminJWK, image, err := readPrior(o.OutDir)
	if err != nil {
		t.Fatal(err)
	}
	if adminJWK != s.Get(config.AdminJWKEnv) {
		t.Errorf("read back the administrator key %q, want %q", adminJWK, s.Get(config.AdminJWKEnv))
	}
	if image != opts.Image {
		t.Errorf("read back the server image %q, want %q", image, opts.Image)
	}
	if got := priorUIImage(o.OutDir); got != opts.UIImage {
		t.Errorf("read back the UI image %q, want %q", got, opts.UIImage)
	}
	for _, key := range []string{config.SigningKeyEnv, config.KeycloakClientKeyEnv, "POSTGRES_PASSWORD", "KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD"} {
		if prior[key] != s.Get(key) {
			t.Errorf("%s did not read back from the Secrets", key)
		}
	}
	for service, want := range map[string]int{"confighub-api": 32180, "confighub-oci-server": 32181, "confighub-ui": 32183, config.KeycloakServiceName: 32182} {
		if got := renderedNodePort(o.OutDir, service); got != want {
			t.Errorf("%s's node port read back as %d, want %d", service, got, want)
		}
	}
	if !hasKeycloak(o.OutDir) {
		t.Error("the render's Keycloak was not found")
	}
	if on, err := renderedMetrics(o.OutDir); err != nil || !on {
		t.Errorf("the render's metrics annotations read back as %v (%v)", on, err)
	}
	settings, err := renderedConfigMapData(o.OutDir, serverConfigMap)
	if err != nil {
		t.Fatal(err)
	}
	if settings["KEYCLOAK_AUTH_URL"] != "http://localhost:32182" {
		t.Errorf("KEYCLOAK_AUTH_URL is %q", settings["KEYCLOAK_AUTH_URL"])
	}

	// The Secrets go with the Namespace, before what reads them.
	waves, err := syncWaves(renderedPaths(t, o.OutDir))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Join(names(waves[0]), ",")
	for _, want := range []string{"confighub-secret-env", "confighub-keycloak-secret"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first wave applies %s, without %s", first, want)
		}
	}

	// Rendering with this plugin's manifests again forgets the installer.
	o.Installer = false
	if _, err := render(ctx, UI{Out: &strings.Builder{}}, o, opts, s); err != nil {
		t.Fatal(err)
	}
	if renderedWithInstaller(o.OutDir) {
		t.Error("still reads back as rendered with the installer")
	}
}

func renderedPaths(t *testing.T, outDir string) []string {
	t.Helper()
	var paths []string
	for _, dir := range []string{config.ConfigDir, config.SecretsDir} {
		found, err := filepath.Glob(filepath.Join(outDir, dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, found...)
	}
	slices.Sort(paths)
	return paths
}
