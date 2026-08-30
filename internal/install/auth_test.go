package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CUB_CONFIG names the config directory. That is what cub sets when it execs a
// plugin, what the docs say, and since confighub#5224 what cubapi resolves from.
//
// The directory form is the one to test with. cub's pluginEnv appends its own
// CUB_CONFIG after os.Environ(), so a test that exports the file form shadows
// it and never exercises the case that actually reaches a plugin.
func TestCubStoreResolvesTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("apiVersion: v1\nkind: Config\ncontexts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUB_CONFIG", dir)

	store, err := CubStore()
	if err != nil {
		t.Fatalf("a directory should be accepted: %v", err)
	}
	if store.ConfigPath() != configPath {
		t.Errorf("ConfigPath = %q, want %q", store.ConfigPath(), configPath)
	}
}

// The file form used to work and no longer does. It should fail as the mistake
// it is, naming the directory to use, rather than surfacing later as a confusing
// read error deeper in.
func TestCubStoreRejectsTheFileForm(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("apiVersion: v1\nkind: Config\ncontexts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUB_CONFIG", configPath)

	_, err := CubStore()
	if err == nil {
		t.Fatal("CUB_CONFIG naming a file should be refused")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error should name the directory to use instead, got: %v", err)
	}
}

// The louder failure was the read error. The quieter one was where keys would
// have gone: the key directory is a sibling of the config file, so a directory
// mistaken for a file puts keys one level too high — ~/keys rather than
// ~/.confighub/keys.
func TestCubStoreResolvesTheKeyDirectoryUnderTheConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("apiVersion: v1\nkind: Config\ncontexts: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUB_CONFIG", dir)

	store, err := CubStore()
	if err != nil {
		t.Fatal(err)
	}
	got := store.KeyPath("admin")
	want := filepath.Join(dir, "keys", "admin.jwk")
	if got != want {
		t.Errorf("KeyPath = %q, want %q", got, want)
	}
}

// A directory with no config.yaml yet is the first-run case and must still work:
// the store is empty but usable, and the install is about to write a key into it.
func TestCubStoreOnADirectoryWithNoConfigYet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CUB_CONFIG", dir)

	store, err := CubStore()
	if err != nil {
		t.Fatalf("a fresh config directory should be usable: %v", err)
	}
	if store.ConfigPath() != filepath.Join(dir, "config.yaml") {
		t.Errorf("ConfigPath = %q", store.ConfigPath())
	}
}

// The command printed for a manual sign-in must be the command that works. cub
// rejects the detached flag spelling, and this printed it while login() used the
// attached one -- so the installer handed the reader a failing command.
func TestManualLoginInstructionsUseTheFormCubAccepts(t *testing.T) {
	lines := manualLoginInstructions("http://localhost:32180", "confighub-admin")
	if len(lines) == 0 {
		t.Fatal("no instructions")
	}
	login := lines[0]
	if !strings.Contains(login, "--private-key=confighub-admin") {
		t.Errorf("must use the attached form cub requires, got: %s", login)
	}
	if strings.Contains(login, "--private-key ") {
		t.Errorf("detached form is rejected by cub, got: %s", login)
	}
}
