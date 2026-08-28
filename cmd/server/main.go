package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/confighub/cub-server/internal/install"
)

func main() {
	// When cub installs/upgrades this binary as a plugin it runs it as a hook to
	// collect the command manifest; handle that and exit. Otherwise run normally —
	// the binary works both as `cub server …` and as a standalone `cub-server …`.
	if handlePluginHook() {
		return
	}
	if err := rootCmd.Execute(); err != nil {
		// ErrReported means the command already printed a readable explanation;
		// don't tack a generic "error:" line on top of it.
		if !errors.Is(err, install.ErrReported) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}
