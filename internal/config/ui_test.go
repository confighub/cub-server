package config

import (
	"strings"
	"testing"
)

func renderFiles(t *testing.T, opts Options) map[string]string {
	t.Helper()
	opts.Defaults()
	s, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Content)
	}
	return out
}

// Each Deployment restarts on a hash of what it reads, so a value belongs to the
// container that reads it. The client id is the UI's; the issuer and audience
// are what the server checks.
func TestUIValuesGoToTheUIContainer(t *testing.T) {
	opts := Options{
		Namespace: "confighub", Image: "img", UIImage: "ghcr.io/confighub/ui:0.6.5",
		APIURL: "http://localhost:32180", Keycloak: testKeycloak(),
	}
	opts.Defaults()
	s, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := func(vars []Var, name string) bool {
		for _, v := range vars {
			if v.Name == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"CONFIGHUB_URL", "CONFIGHUB_UI_OAUTH_CLIENT_ID"} {
		if !in(s.UIConfigMapVars(), name) {
			t.Errorf("%s is not in the UI's ConfigMap", name)
		}
		if in(s.ConfigMapVars(), name) {
			t.Errorf("%s is in the server's ConfigMap; the server does not need it", name)
		}
	}
	for _, name := range []string{"CONFIGHUB_AUTH_ISSUER", "CONFIGHUB_TOKEN_EXCHANGE_AUDIENCE"} {
		if !in(s.ConfigMapVars(), name) {
			t.Errorf("%s is not in the server's ConfigMap", name)
		}
	}
	// Read by no server this plugin installs: cookie login is gone.
	if s.Get("KEYCLOAK_REDIRECT_URI") != "" {
		t.Error("KEYCLOAK_REDIRECT_URI is rendered, but the server no longer reads it")
	}
	if got := s.Get("CONFIGHUB_URL"); got != "http://localhost:32180" {
		t.Errorf("CONFIGHUB_URL = %q, want the browser's address of the API", got)
	}
}

func TestUIIsRenderedAsItsOwnDeploymentAndService(t *testing.T) {
	files := renderFiles(t, Options{
		Namespace: "ns", Image: "img", UIImage: "ghcr.io/confighub/ui:0.6.5",
		APIURL: "http://localhost:32180", APINodePort: 32180, OCINodePort: 32181, UINodePort: 32183,
	})

	ui, ok := files[ConfigDir+"/45-ui.yaml"]
	if !ok {
		t.Fatal("no UI manifest rendered")
	}
	for _, want := range []string{
		"image: ghcr.io/confighub/ui:0.6.5",
		"CONFIGHUB_URL: http://localhost:32180",
		"namespace: ns",
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("UI manifest lacks %q:\n%s", want, ui)
		}
	}
	if strings.Contains(ui, `config-hash: ""`) {
		t.Error("the UI Deployment carries no config hash, so a changed ConfigMap would not restart it")
	}

	svc := files[ConfigDir+"/50-service.yaml"]
	if !strings.Contains(svc, "name: confighub-ui") || !strings.Contains(svc, "nodePort: 32183") {
		t.Errorf("the UI Service is missing or not on its NodePort:\n%s", svc)
	}
}

func TestRenderRefusesAMissingUIImage(t *testing.T) {
	opts := Options{Namespace: "ns", Image: "img"}
	opts.Defaults()
	s, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(s, opts); err == nil {
		t.Fatal("rendered an instance with no UI image")
	}
}
