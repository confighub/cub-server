package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var keycloakOpenArgs struct {
	printURL      bool
	printPassword bool
}

var keycloakOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open Keycloak's admin console in the browser",
	Long: `Open the admin console of this instance's Keycloak, log-in credentials in hand:
the admin password is copied to your clipboard so you can paste it into the
login form instead of digging it out of the cluster.

This is where people are added. A ConfigHub user is a user in this instance's
realm who is a member of its organization -- both parts matter, because someone
who is in the realm but no organization can sign in and is then told their
account is pending approval.

The password is the one Keycloak was given on its first start. It is Keycloak's
own administrator, not a ConfigHub account: it opens this console and nothing
else, and the ConfigHub server never reads it.

Examples:
  cub server keycloak open                    open the console, password on the clipboard
  cub server keycloak open --print-password   print the password instead of copying it
  cub server keycloak open --print-url        print the URL instead of opening a browser`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		err := install.OpenKeycloak(cmd.Context(), cmd.OutOrStdout(), &keycloakOpenOpts,
			keycloakOpenArgs.printURL, keycloakOpenArgs.printPassword)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nFailed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

// Its own options rather than keycloakInstallCmd's, so a flag set on one
// command cannot be read by the other.
var keycloakOpenOpts install.Options

func init() {
	f := keycloakOpenCmd.Flags()
	f.BoolVar(&keycloakOpenArgs.printURL, "print-url", false, "Print the URL instead of opening a browser")
	f.BoolVar(&keycloakOpenArgs.printPassword, "print-password", false,
		"Print the admin password to the terminal instead of copying it to the clipboard")

	// The flags that locate the instance, matching `keycloak install`.
	f.StringVar(&keycloakOpenOpts.OutDir, "out-dir", "", "Directory holding the instance's generated manifests")
	f.StringVar(&keycloakOpenOpts.Namespace, "namespace", "", "Namespace the instance is in")
	f.StringVar((*string)(&keycloakOpenOpts.Target), "target", "", "Where the instance runs: kind or context")
	f.StringVar(&keycloakOpenOpts.ClusterName, "cluster-name", "", "kind cluster the instance is in")
	f.StringVar(&keycloakOpenOpts.KubeContext, "kube-context", "", "Kubeconfig context the instance is in")

	keycloakCmd.AddCommand(keycloakOpenCmd)
}
