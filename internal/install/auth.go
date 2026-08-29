package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/confighub/sdk/core/cubapi"
)

// The install ends authenticated, which is the point of doing it here rather
// than leaving the operator a key and a URL.
//
// Two halves, deliberately split. Writing the private key uses cub's own key
// store directly, so the file lands exactly where `cub auth login
// --private-key` looks for it, with that command's permissions and its
// refuse-to-overwrite rule. Logging in shells out to cub, because minting a
// session means signing an assertion and exchanging it, and reimplementing that
// here would be a second implementation of the thing most worth having only one
// of.

// CubStore opens cub's own configuration, from wherever this process is being
// run from.
//
// CUB_CONFIG does not mean the same thing to everyone, which is the trap this
// exists to avoid. cubapi reads it as the path of the config *file*
// (cubapi.DefaultConfigPath). But cub, when it execs a plugin, sets it to the
// config *directory* -- `CUB_CONFIG=%s` over `filepath.Dir(ConfigPath())` in
// cub's pluginEnv. So a plugin that calls cubapi.LoadConfig("") is handed a
// directory where a file is expected.
//
// It fails loudly on the read ("is a directory"), but the quieter half is worse:
// the key directory is a sibling of the config file, so a directory taken for a
// file would put keys in ~/keys rather than ~/.confighub/keys.
//
// Accepting both spellings here rather than picking one: cub-server has to work
// with the cub that is installed, and both readings are live.
func CubStore() (*cubapi.Store, error) {
	path := os.Getenv("CUB_CONFIG")
	if path == "" {
		return cubapi.LoadConfig("")
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, cubapi.ConfigFileName)
	}
	return cubapi.LoadConfig(path)
}

// writeAdminKey stores the generated private key under cub's key directory and
// returns the path.
//
// The private half never reaches the cluster; that is what makes the public half
// safe to sit in a ConfigMap.
func writeAdminKey(name, privateJWK string) (string, error) {
	store, err := CubStore()
	if err != nil {
		return "", err
	}
	return store.WritePrivateKey(name, json.RawMessage(privateJWK))
}

// cubBinary finds cub.
//
// A plugin is exec'd by cub, but not necessarily from a directory on PATH, and
// the plugin contract passes no path back. So this is a lookup that can fail,
// and failing is not fatal: the install has already succeeded by this point, and
// the caller falls back to printing the commands to run.
func cubBinary() (string, error) {
	return exec.LookPath("cub")
}

// activeContextName reports which cub context is current, after a login.
//
// The context is not created here. `cub auth login` creates and names it, and
// it is the only party that can: a context is keyed on server, organization and
// user, and the last two are not known until the login has resolved them. An
// earlier version of this created a context up front and had cub quietly
// replace it, leaving a stray behind.
func activeContextName() string {
	store, err := CubStore()
	if err != nil {
		return ""
	}
	return store.CurrentContextName()
}

// login exchanges the administrator's key for a session, leaving the caller able
// to run cub commands immediately.
//
// --new-context, because an install produces a new instance and must not adopt
// whichever context happens to be current. Without it, `cub auth login` writes
// into the active context when that context is not itself authenticated, which
// silently repoints a context the operator uses for something else at the
// instance just installed.
//
// The cost is that reinstalling the same instance leaves an extra context
// behind. That is untidy where the alternative is destructive.
func login(ctx context.Context, cub, serverURL, keyName string) error {
	// Attached form: cub requires --private-key=value, and rejects the detached
	// spelling rather than consuming the next argument.
	_, err := run(ctx, cub, "auth", "login",
		"--new-context",
		"--private-key="+keyName,
		"--server="+serverURL,
	)
	return err
}

// verify makes one authenticated call, so that "installed" means "answered a
// real request" rather than "the pod is running".
func verify(ctx context.Context, cub string) error {
	_, err := run(ctx, cub, "space", "list")
	return err
}

// keyExists reports whether a key of this name is already in cub's store.
//
// WritePrivateKey refuses to overwrite, which is the behaviour we want, but on a
// re-run that refusal would abort an install that is otherwise fine. Checking
// first lets a re-run say "reusing the existing key" instead.
func keyExists(name string) (string, bool) {
	store, err := CubStore()
	if err != nil {
		return "", false
	}
	path := store.KeyPath(name)
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// manualLoginInstructions is what to print when cub could not be found, so an
// otherwise successful install still ends with the user knowing what to type.
// The attached flag form, matching what login() runs. cub rejects the detached
// spelling, so printing it hands the reader a command that fails.
func manualLoginInstructions(serverURL, keyName string) []string {
	return []string{
		fmt.Sprintf("cub auth login --private-key=%s --server=%s", keyName, serverURL),
		"cub space list",
	}
}
