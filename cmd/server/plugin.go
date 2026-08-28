package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// cub plugin contract (see cub's cmd/cub/plugin_exec.go):
//
//   - On install/upgrade, cub runs this binary with CUB_PLUGIN_HOOK=<phase> and
//     CUB_PLUGIN_DIR=<plugin dir> set. The binary must write a cub-plugin.yaml
//     manifest into that dir declaring the command(s) it provides, then exit.
//   - On `cub server …`, cub execs this binary with the remaining args and
//     injects CUB_SERVER / CUB_TOKEN / CUB_CONTEXT / CUB_SPACE / CUB_CONFIG. The
//     binary is a normal cobra app, so `cub server install …` runs the install
//     subcommand.
//
// The env var names are hard-coded (matching cub) so the plugin builds without
// depending on cub itself.
const (
	envPluginHook = "CUB_PLUGIN_HOOK"
	envPluginDir  = "CUB_PLUGIN_DIR"

	// pluginCommand is what users type after `cub`.
	pluginCommand = "server"
)

// handlePluginHook writes cub-plugin.yaml when cub invokes this binary as an
// install/upgrade hook, and reports whether it did (so main can exit early).
func handlePluginHook() bool {
	if os.Getenv(envPluginHook) == "" {
		return false
	}
	dir := os.Getenv(envPluginDir)
	if dir == "" {
		fmt.Fprintln(os.Stderr, envPluginHook+" set but "+envPluginDir+" is empty")
		os.Exit(1)
	}

	// entrypoint is this binary's own filename within the plugin dir.
	entrypoint := filepath.Base(os.Args[0])
	manifest := fmt.Sprintf(`name: %s
version: %s
commands:
  - name: %s
    summary: Install and manage a self-hosted ConfigHub server
    entrypoint: %s
`, pluginCommand, version, pluginCommand, entrypoint)

	if err := os.WriteFile(filepath.Join(dir, "cub-plugin.yaml"), []byte(manifest), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write cub-plugin.yaml:", err)
		os.Exit(1)
	}
	return true
}
