package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var checkOpts install.CheckOptions

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check that a running instance can be reached where it says it is",
	Long: `Check that a running instance can be reached where it says it is.

It asks the API where the UI, the OCI registry and the identity provider are
(GET /api/info), then tries each one from this machine. Nothing is changed and
nothing is read from the cluster, so it works however the instance was
deployed.

Run it after changing how the instance is exposed. The registry matters most:
'cub cluster attach' gives Argo CD and Flux the address the server advertises,
and an address left over from the install, such as a NodePort, is only found
when a cluster fails to pull.

Examples:
  cub server check                                         the server of cub's current context
  cub server check --url https://confighub-api.example.com`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.RunCheck(cmd.Context(), ui, &checkOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nCheck failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	checkCmd.Flags().StringVar(&checkOpts.URL, "url", "", "Where the API answers (default: the server of cub's current context)")
	rootCmd.AddCommand(checkCmd)
}
