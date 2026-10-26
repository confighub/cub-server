package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

func configureOptionsFor(t *testing.T) *ConfigureOptions {
	t.Helper()
	c := &ConfigureOptions{
		Keycloak: config.Keycloak{PublicURL: "https://kc.acme.com"},
		UIURL:    "https://confighub.acme.com",
		OutDir:   t.TempDir(),
	}
	return c
}

// Bootstrap makes the key once and finds it again on a re-run: Keycloak holds
// its public half, so a new one would cut off a server already using the old.
func TestBootstrapClientKeyIsKeptBetweenRuns(t *testing.T) {
	kept := filepath.Join(t.TempDir(), clientKeyFile)
	first, generated, err := bootstrapClientKey("", kept)
	if err != nil || !generated {
		t.Fatalf("first run: generated=%v err=%v", generated, err)
	}
	if err := writeFile(kept, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, generated, err := bootstrapClientKey("", kept)
	if err != nil || generated || second != first {
		t.Fatalf("second run: generated=%v err=%v same=%v", generated, err, second == first)
	}
	if _, _, err := bootstrapClientKey(filepath.Join(t.TempDir(), "missing.jwk"), kept); err == nil {
		t.Fatal("a missing --client-key-file made a new key")
	}
}

// Configure never makes a key: it signs in with bootstrap's, and without one
// it says to run bootstrap.
func TestConfigureNeedsBootstrapsKey(t *testing.T) {
	c := configureOptionsFor(t)
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.clientKey(); err == nil || !strings.Contains(err.Error(), "keycloak bootstrap") {
		t.Fatalf("no key: err = %v, want it to point at bootstrap", err)
	}
	key, _ := config.GenerateClientKey()
	if err := writeFile(filepath.Join(c.OutDir, clientKeyFile), []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := c.clientKey(); err != nil || got != key {
		t.Fatalf("with bootstrap's key: err=%v same=%v", err, got == key)
	}
}

// Without --out-dir, the patches and the key go under ~/.confighub, never into
// whatever directory the command happens to run in.
func TestConfigureDefaultsOutDir(t *testing.T) {
	c := configureOptionsFor(t)
	c.OutDir = ""
	t.Setenv("HOME", t.TempDir())
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.OutDir, filepath.Join(".confighub", "keycloak", "kc.acme.com", "confighub")) {
		t.Errorf("OutDir = %q", c.OutDir)
	}
}

// With an ExternalSecret owning the server's Secret, a Secret patch would be
// undone at the next sync, so none is written and the key goes to an entry
// instead.
func TestExternalSecretsOutputs(t *testing.T) {
	c := configureOptionsFor(t)
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	key, err := config.GenerateClientKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.writeOutputs(key); err != nil {
		t.Fatal(err)
	}

	c.Secrets, c.RemoteKey, c.RemoteProperty = SecretsExternal, "confighub/server", "KEYCLOAK_CLIENT_PRIVATE_KEY_JWK"
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	out, err := c.writeOutputs(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out.secretPatch); !os.IsNotExist(err) {
		t.Error("the Secret patch from the earlier run is still there")
	}

	raw, err := os.ReadFile(out.externalSecretPatch)
	if err != nil {
		t.Fatal(err)
	}
	var patch []struct {
		Op, Path string
		Value    struct {
			SecretKey string            `json:"secretKey"`
			RemoteRef map[string]string `json:"remoteRef"`
		}
	}
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || patch[0].Path != "/spec/data/-" || patch[0].Value.SecretKey != config.KeycloakClientKeyEnv ||
		patch[0].Value.RemoteRef["key"] != "confighub/server" || patch[0].Value.RemoteRef["property"] != "KEYCLOAK_CLIENT_PRIVATE_KEY_JWK" {
		t.Errorf("patch = %s", raw)
	}

	entry, _ := os.ReadFile(out.externalSecretEntry)
	if !strings.Contains(string(entry), `key: "confighub/server"`) {
		t.Errorf("entry = %s", entry)
	}
	if stored, _ := os.ReadFile(out.clientKey); strings.TrimSpace(string(stored)) != key {
		t.Error("the key file does not hold the key")
	}
	if info, _ := os.Stat(out.clientKey); info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", info.Mode().Perm())
	}
}

func TestSecretsModeValidation(t *testing.T) {
	c := configureOptionsFor(t)
	c.Secrets = SecretsExternal
	if err := c.defaults(); err == nil {
		t.Error("accepted external-secrets with no --remote-key")
	}
	c = configureOptionsFor(t)
	c.RemoteKey = "x"
	if err := c.defaults(); err == nil {
		t.Error("accepted --remote-key without external-secrets")
	}
}

// The Secrets Store CSI driver takes the key from AWS Secrets Manager into the
// server's Secret, and writes that Secret only while a pod mounts the class.
func TestSecretsStoreCSIOutputs(t *testing.T) {
	c := configureOptionsFor(t)
	c.Secrets, c.SecretProviderClass = SecretsStoreCSI, "confighub"
	c.RemoteKey, c.RemoteProperty = "confighub/server", "KEYCLOAK_CLIENT_PRIVATE_KEY_JWK"
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	key, err := config.GenerateClientKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.writeOutputs(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out.secretPatch); !os.IsNotExist(err) {
		t.Error("a Secret patch was written for a Secret the driver owns")
	}
	raw, err := os.ReadFile(out.secretProviderClass)
	if err != nil {
		t.Fatal(err)
	}

	// Each fragment parses on its own, and says what the driver needs.
	docs := strings.Split(string(raw), "\n---\n")
	if len(docs) != 4 {
		t.Fatalf("%d fragments, want 4:\n%s", len(docs), raw)
	}
	var objects []struct {
		ObjectName string `yaml:"objectName"`
		ObjectType string `yaml:"objectType"`
		JMESPath   []struct {
			Path        string `yaml:"path"`
			ObjectAlias string `yaml:"objectAlias"`
		} `yaml:"jmesPath"`
	}
	if err := yaml.Unmarshal([]byte(docs[0]), &objects); err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].ObjectName != "confighub/server" || objects[0].ObjectType != "secretsmanager" ||
		len(objects[0].JMESPath) != 1 || objects[0].JMESPath[0].ObjectAlias != config.KeycloakClientKeyEnv {
		t.Errorf("objects entry = %+v", objects)
	}
	var data []map[string]string
	if err := yaml.Unmarshal([]byte(docs[1]), &data); err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 || data[0]["objectName"] != config.KeycloakClientKeyEnv || data[0]["key"] != config.KeycloakClientKeyEnv {
		t.Errorf("secretObjects data = %v", data)
	}
	if !strings.Contains(docs[2], `secretProviderClass: "confighub"`) {
		t.Errorf("volume = %s", docs[2])
	}
	for _, doc := range docs[2:] {
		var v any
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			t.Errorf("fragment does not parse: %v\n%s", err, doc)
		}
	}
}

func TestSecretsStoreCSINeedsAClass(t *testing.T) {
	c := configureOptionsFor(t)
	c.Secrets, c.RemoteKey = SecretsStoreCSI, "confighub/server"
	if err := c.defaults(); err == nil {
		t.Error("accepted secrets-store-csi with no --secret-provider-class")
	}
}
