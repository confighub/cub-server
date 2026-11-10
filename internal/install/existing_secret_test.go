package install

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// An install given --existing-secret records it in its render, and a re-run
// without the flag keeps it, so it never goes back to applying a Secret of its
// own. --existing-secret="" is how to.
func TestRerunKeepsTheExistingSecret(t *testing.T) {
	dir := t.TempDir()
	writeOptionsRender(t, dir, config.Options{Namespace: "confighub", APINodePort: 32180, ExistingSecret: "from-vault"})

	o := Options{KubeContext: "prod", OutDir: dir}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.ExistingSecret != "from-vault" || o.deploymentOptions().ExistingSecret != "from-vault" {
		t.Errorf("a re-run without the flag reads the Secret %q", o.ExistingSecret)
	}

	o = Options{KubeContext: "prod", OutDir: dir, ExistingSecretSet: true}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.ExistingSecret != "" {
		t.Errorf("--existing-secret=\"\" kept %q", o.ExistingSecret)
	}

	// An ordinary render records none.
	plain := t.TempDir()
	writeOptionsRender(t, plain, config.Options{Namespace: "confighub", APINodePort: 32180})
	if name, err := recordedExistingSecret(plain); err != nil || name != "" {
		t.Errorf("an ordinary render records %q, %v", name, err)
	}
}

// The operator's Secret and the values file are never applied; everything else
// is.
func TestAppliedFilesLeavesTheOperatorsSecretOut(t *testing.T) {
	files := []config.File{
		{Path: config.ConfigDir + "/40-deployment.yaml"},
		{Path: config.SecretsDir + "/" + config.SecretFile},
		{Path: config.SecretsDir + "/" + config.SecretValuesFile},
		{Path: config.SecretsDir + "/" + config.KeycloakSecretFile},
	}
	paths := func(fs []config.File) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	if got := appliedFiles(&Options{}, files); len(got) != len(files) {
		t.Errorf("without --existing-secret, applied %v", paths(got))
	}
	got := paths(appliedFiles(&Options{ExistingSecret: "from-vault"}, files))
	want := []string{config.ConfigDir + "/40-deployment.yaml", config.SecretsDir + "/" + config.KeycloakSecretFile}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with --existing-secret, applied %v, want %v", got, want)
	}
}

func TestSecretProblems(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	recorded := map[string]string{
		config.SigningKeyEnv:        "signing",
		"POSTGRES_PASSWORD":         "pw",
		"DATABASE_URL":              "postgresql://...",
		config.KeycloakClientKeyEnv: "client-key",
	}

	// Every key, the operator's own values for all but the pinned one.
	live := map[string]string{
		config.SigningKeyEnv:        b64("their-signing"),
		"POSTGRES_PASSWORD":         b64("their-pw"),
		"DATABASE_URL":              b64("their-url"),
		config.KeycloakClientKeyEnv: b64("client-key"),
	}
	if missing, differ, err := secretProblems(recorded, live); err != nil || missing != nil || differ != nil {
		t.Errorf("a complete Secret: missing %v, differ %v, %v", missing, differ, err)
	}

	// An empty key is as good as a missing one; a pinned value of the
	// operator's own is refused.
	live["POSTGRES_PASSWORD"] = ""
	delete(live, "DATABASE_URL")
	live[config.KeycloakClientKeyEnv] = b64("another-key")
	missing, differ, err := secretProblems(recorded, live)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DATABASE_URL", "POSTGRES_PASSWORD"}; !reflect.DeepEqual(missing, want) {
		t.Errorf("missing %v, want %v", missing, want)
	}
	if want := []string{config.KeycloakClientKeyEnv}; !reflect.DeepEqual(differ, want) {
		t.Errorf("differ %v, want %v", differ, want)
	}

	// An external database whose URL install was never given still needs one.
	if got := requiredSecretKeys(map[string]string{config.SigningKeyEnv: "x"}); !reflect.DeepEqual(got, []string{config.SigningKeyEnv, "DATABASE_URL"}) {
		t.Errorf("required %v", got)
	}
}

// The installer package names the Secret itself.
func TestInstallerTakesOnlyThePackagesSecretName(t *testing.T) {
	for name, ok := range map[string]bool{"": true, config.DefaultSecretName: true, "from-vault": false} {
		o := Options{KubeContext: "prod", OutDir: t.TempDir(), Installer: true, InstallerSet: true, ExistingSecret: name, ExistingSecretSet: true}
		if err := o.Defaults(); err != nil {
			t.Fatal(err)
		}
		o.APINodePort, o.OCINodePort, o.UINodePort = 32180, 32181, 32183
		err := o.Validate()
		if ok != (err == nil) {
			t.Errorf("--installer --existing-secret %q: %v", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), config.DefaultSecretName) {
			t.Errorf("the refusal does not say which name: %v", err)
		}
	}
}
