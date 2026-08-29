package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// uninstall leaves the administrator's private key behind on purpose -- it is a
// credential, not deployment state -- so a later install must adopt it rather
// than mint a second key that collides with it by name. That collision is what
// made uninstall followed by install fail.
func TestInstallReusesAKeyLeftByUninstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CUB_CONFIG", dir)

	pair, err := config.GenerateAdminKeypair()
	if err != nil {
		t.Fatal(err)
	}
	store, err := CubStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WritePrivateKey("confighub-admin", json.RawMessage(pair.PrivateJWK)); err != nil {
		t.Fatal(err)
	}

	public, path, err := reusableAdminKey("confighub-admin")
	if err != nil {
		t.Fatalf("an install after an uninstall must be able to reuse the key: %v", err)
	}
	if path == "" {
		t.Fatal("the existing key was not found")
	}

	// The derived public half must be the one the instance would have trusted,
	// or the key the operator holds cannot sign in to it.
	if err := config.ValidateAdminPublicJWK(public); err != nil {
		t.Errorf("derived public key is not usable: %v", err)
	}
	if !jsonEqual(t, public, pair.PublicJWK) {
		t.Errorf("derived public key differs from the generated one:\n got %s\nwant %s", public, pair.PublicJWK)
	}
	if strings.Contains(public, `"d"`) {
		t.Error("the derived public key still carries private material")
	}
}

// No key is the ordinary first install, and must not be an error.
func TestReusableAdminKeyOnAFreshMachine(t *testing.T) {
	t.Setenv("CUB_CONFIG", t.TempDir())
	public, path, err := reusableAdminKey("confighub-admin")
	if err != nil {
		t.Fatalf("a first install should not error: %v", err)
	}
	if public != "" || path != "" {
		t.Errorf("expected nothing found, got %q at %q", public, path)
	}
}

// A key that cannot yield a public half is refused rather than silently
// regenerated, since regenerating under a taken name is the original failure.
func TestReusableAdminKeyRefusesAnUnusableKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CUB_CONFIG", dir)
	if err := os.MkdirAll(filepath.Join(dir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "broken.jwk"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reusableAdminKey("broken"); err == nil {
		t.Fatal("expected an error for an unreadable key")
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y map[string]any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatal(err)
	}
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if y[k] != v {
			return false
		}
	}
	return true
}

// cub passes the caller's context, server, token and space to a plugin through
// the environment. They describe the instance the operator was already talking
// to, not the one being installed, and CUB_CONTEXT is an override -- a cub run
// with it set ignores the current context entirely.
//
// Left in, a login that correctly created a new context is followed by a call
// that addresses the old one, against a server that cannot verify its token.
func TestCubEnvDropsTheCallersPosition(t *testing.T) {
	t.Setenv("CUB_CONTEXT", "somewhere-else")
	t.Setenv("CUB_TOKEN", "a-token-for-another-instance")
	t.Setenv("CUB_SERVER", "https://hub.example.com")
	t.Setenv("CUB_SPACE", "their-space")
	t.Setenv("CUB_CONFIG", "/tmp/some-config")
	t.Setenv("PATH", os.Getenv("PATH"))

	seen := map[string]string{}
	for _, kv := range cubEnv() {
		if name, value, ok := strings.Cut(kv, "="); ok {
			seen[name] = value
		}
	}

	for _, name := range []string{"CUB_CONTEXT", "CUB_TOKEN", "CUB_SERVER", "CUB_SPACE"} {
		if v, present := seen[name]; present {
			t.Errorf("%s=%q was passed through; it addresses the caller's instance, not this one", name, v)
		}
	}

	// CUB_CONFIG names the store rather than a position in it, and the child has
	// to read and write the same one.
	if seen["CUB_CONFIG"] != "/tmp/some-config" {
		t.Errorf("CUB_CONFIG = %q, want it preserved", seen["CUB_CONFIG"])
	}
	if seen["PATH"] == "" {
		t.Error("PATH was dropped; the child could not find anything")
	}
}
