package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// RenderSecrets renders the Secrets on their own, for an install whose other
// manifests come from the installer package, in the sync wave asked for: they
// have to be applied before Postgres and Keycloak, which read them as they start.
func TestRenderSecrets(t *testing.T) {
	opts := Options{Namespace: "confighub", Image: "ghcr.io/confighubai/confighub:test", UIImage: "ghcr.io/confighub/ui:test",
		Keycloak: &Keycloak{PublicURL: "http://localhost:32182", UIRedirectURI: "http://localhost:32183/"}}
	opts.Defaults()
	s, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := RenderSecrets(s, opts, "-2")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{SecretsDir + "/" + SecretFile: "confighub-secret-env", SecretsDir + "/" + KeycloakSecretFile: "confighub-keycloak-secret"}
	if len(files) != len(want) {
		t.Fatalf("rendered %d files, want %d", len(files), len(want))
	}
	for _, f := range files {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name        string            `yaml:"name"`
				Namespace   string            `yaml:"namespace"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
			StringData map[string]string `yaml:"stringData"`
		}
		if err := yaml.Unmarshal(f.Content, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Kind != "Secret" || doc.Metadata.Name != want[f.Path] || doc.Metadata.Namespace != "confighub" || !f.Sensitive {
			t.Errorf("%s: a %s %s/%s, sensitive %v", f.Path, doc.Kind, doc.Metadata.Namespace, doc.Metadata.Name, f.Sensitive)
		}
		if got := doc.Metadata.Annotations[syncWaveAnnotation]; got != "-2" {
			t.Errorf("%s: sync wave %q, want -2", f.Path, got)
		}
		if len(doc.StringData) == 0 {
			t.Errorf("%s: no values", f.Path)
		}
	}

	// Without a wave, the Secrets are as Render writes them.
	files, err = RenderSecrets(s, opts, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files[0].Content), syncWaveAnnotation) {
		t.Errorf("a sync wave without one being asked for:\n%s", files[0].Content)
	}
}
