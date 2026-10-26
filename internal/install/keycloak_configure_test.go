package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// The key is what Keycloak holds the public half of, so a re-run must find the
// one the last run made rather than rotate it.
func TestClientKeyIsKeptBetweenRuns(t *testing.T) {
	c := configureOptionsFor(t)
	if err := c.defaults(); err != nil {
		t.Fatal(err)
	}
	first, generated, err := c.clientKey()
	if err != nil || !generated {
		t.Fatalf("first run: generated=%v err=%v", generated, err)
	}
	if _, err := c.writeOutputs(first); err != nil {
		t.Fatal(err)
	}
	second, generated, err := c.clientKey()
	if err != nil || generated || second != first {
		t.Fatalf("second run: generated=%v err=%v same=%v", generated, err, second == first)
	}

	// A key file passed in wins, and one that is missing is an error rather
	// than a reason to make a new key.
	c.ClientKeyFile = filepath.Join(c.OutDir, clientKeyFile)
	if got, _, err := c.clientKey(); err != nil || got != first {
		t.Fatalf("--client-key-file: err=%v same=%v", err, got == first)
	}
	c.ClientKeyFile = filepath.Join(c.OutDir, "missing.jwk")
	if _, _, err := c.clientKey(); err == nil {
		t.Fatal("a missing --client-key-file made a new key")
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
	key, _, err := c.clientKey()
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
