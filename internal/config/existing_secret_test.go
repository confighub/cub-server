package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// With a Secret the operator provides, every workload that reads the server's
// Secret names it instead, and the rendered one carries that name and the mark
// that says install does not apply it.
func TestExistingSecretIsWhatTheWorkloadsRead(t *testing.T) {
	opts := ociTestOptions("")
	opts.ExistingSecret = "confighub-from-vault"
	files := renderFiles(t, opts)

	for _, path := range []string{ConfigDir + "/40-deployment.yaml", ConfigDir + "/30-database.yaml"} {
		if strings.Contains(files[path], "name: "+DefaultSecretName) {
			t.Errorf("%s still reads %s:\n%s", path, DefaultSecretName, files[path])
		}
	}
	if n := strings.Count(files[ConfigDir+"/40-deployment.yaml"], "name: confighub-from-vault"); n != 2 {
		t.Errorf("the server's two containers name the Secret %d times, want 2", n)
	}
	if !strings.Contains(files[ConfigDir+"/30-database.yaml"], "name: confighub-from-vault") {
		t.Errorf("the database does not read its password from the operator's Secret")
	}

	var secret struct {
		Metadata struct {
			Name        string            `yaml:"name"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal([]byte(files[SecretsDir+"/"+SecretFile]), &secret); err != nil {
		t.Fatal(err)
	}
	if secret.Metadata.Name != "confighub-from-vault" || secret.Metadata.Annotations[ExistingSecretAnnotation] != "true" {
		t.Errorf("the rendered Secret is %q with annotations %v", secret.Metadata.Name, secret.Metadata.Annotations)
	}

	// The same values, as the flat object a secret store takes.
	var values map[string]string
	if err := json.Unmarshal([]byte(files[SecretsDir+"/"+SecretValuesFile]), &values); err != nil {
		t.Fatalf("%s: %v", SecretValuesFile, err)
	}
	if len(values) == 0 || len(values) != len(secret.StringData) {
		t.Fatalf("%s has %d values, the Secret %d", SecretValuesFile, len(values), len(secret.StringData))
	}
	for k, v := range secret.StringData {
		if values[k] != v {
			t.Errorf("%s: %s differs from the Secret", SecretValuesFile, k)
		}
	}
}

// Without one, nothing changes: the default name, no mark, no values file.
func TestNoExistingSecretRendersAsBefore(t *testing.T) {
	files := renderFiles(t, ociTestOptions(""))
	if _, ok := files[SecretsDir+"/"+SecretValuesFile]; ok {
		t.Errorf("a values file without --existing-secret")
	}
	var secret struct {
		Metadata struct {
			Name        string            `yaml:"name"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(files[SecretsDir+"/"+SecretFile]), &secret); err != nil {
		t.Fatal(err)
	}
	if _, marked := secret.Metadata.Annotations[ExistingSecretAnnotation]; secret.Metadata.Name != DefaultSecretName || marked {
		t.Errorf("the Secret is %q with annotations %v", secret.Metadata.Name, secret.Metadata.Annotations)
	}
}

// DATABASE_URL may be in the operator's Secret rather than on the command line,
// but only there: without one, an external database still needs its URL.
func TestExternalDatabaseURLCanLiveInTheExistingSecret(t *testing.T) {
	opts := ociTestOptions("")
	opts.Database = DatabaseExternal
	opts.Defaults()
	if err := opts.Validate(); err == nil || !strings.Contains(err.Error(), "--database-url") {
		t.Errorf("an external database with no URL and no Secret: %v", err)
	}
	opts.ExistingSecret = "confighub-from-vault"
	if err := opts.Validate(); err != nil {
		t.Errorf("an external database whose URL is in the operator's Secret: %v", err)
	}
	if files := renderFiles(t, opts); strings.Contains(files[SecretsDir+"/"+SecretFile], "DATABASE_URL") {
		t.Errorf("a DATABASE_URL rendered with none given")
	}
}

func TestValidateSecretName(t *testing.T) {
	for _, ok := range []string{"", "confighub-secret-env", "confighub.vault"} {
		if err := ValidateSecretName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ConfigHub", "confighub_secret", "-x"} {
		if err := ValidateSecretName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
