package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var uninstallOpts install.Options
var uninstallKeepConfig bool

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Tear down an installed ConfigHub server",
	Long: `Remove what 'cub server install' created.

For a kind install this deletes the cluster, which takes everything in it
including the database and its data. For an install into an existing cluster it
deletes the namespace and leaves the cluster alone.

The generated configuration is deleted too, unless --keep-config. It holds the
signing key and the worker master secret, so keeping it is what makes a later
reinstall the same instance rather than a new one -- and deleting it is what
makes the teardown complete.

The administrator's private key in cub's key directory is never touched here.
It is a credential rather than deployment state, and it is not this command's to
remove.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.Uninstall(cmd.Context(), ui, &uninstallOpts, uninstallKeepConfig); err != nil {
			fmt.Fprintf(os.Stderr, "\nUninstall failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	f := uninstallCmd.Flags()
	f.StringVar((*string)(&uninstallOpts.Target), "target", "", "kind or context (default kind)")
	f.StringVar(&uninstallOpts.ClusterName, "cluster-name", "", "Name of the kind cluster to delete (default "+install.DefaultClusterName+")")
	f.StringVar(&uninstallOpts.KubeContext, "kube-context", "", "Kubeconfig context to remove the namespace from, with --target=context")
	f.StringVar(&uninstallOpts.Namespace, "namespace", "", "Namespace to delete (default "+install.DefaultNamespace+")")
	f.StringVar(&uninstallOpts.OutDir, "out-dir", "", "Generated config to remove (default ~/.confighub/servers/<name>)")
	f.BoolVar(&uninstallKeepConfig, "keep-config", false, "Leave the generated config in place, so a reinstall is the same instance")

	rootCmd.AddCommand(uninstallCmd)
}
