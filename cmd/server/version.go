package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is the build version, overridable via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("cub-server %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
