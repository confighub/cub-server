package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var installOpts install.Options

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a ConfigHub server and sign in to it",
	Long: `Install a ConfigHub instance and leave you authenticated to it.

By default it creates a local kind cluster, so the only prerequisite is a
running Docker. With --target=context it installs into a cluster you already
have, using the same manifests.

The instance is created with no identity provider. An administrator keypair is
generated during the install: the public half goes into the instance's
configuration, and the private half is written to cub's key directory, where
'cub auth login --private-key' finds it. The command then signs in with it, so
the install ends with a working session rather than with instructions.

Re-running is safe. Generated values -- the token signing key, the worker master
secret, the database password -- are read back from the previous run's output
and reused, because rotating them would log every session out and break every
worker that had enrolled.

Examples:
  cub server install -i                        answer questions, then install
  cub server install                           defaults: a kind cluster
  cub server install --node-port 32200         when 32180 is taken
  cub server install --target=context --kube-context=my-cluster
  cub server install --dry-run                 render the manifests, create nothing`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if installInteractive {
			if err := install.Interview(os.Stdin, os.Stdout, &installOpts); err != nil {
				return err
			}
		}
		if err := install.Run(cmd.Context(), ui, &installOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nInstall failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

var installInteractive bool

func init() {
	f := installCmd.Flags()
	f.BoolVarP(&installInteractive, "interactive", "i", false, "Ask before installing, instead of taking every default")

	f.StringVar((*string)(&installOpts.Target), "target", "", "kind (create a local cluster) or context (use one you have) (default kind)")
	f.StringVar(&installOpts.ClusterName, "cluster-name", "", "Name of the kind cluster to create (default "+install.DefaultClusterName+")")
	f.StringVar(&installOpts.KubeContext, "kube-context", "", "Kubeconfig context to install into, with --target=context")

	f.StringVar(&installOpts.Namespace, "namespace", "", "Namespace to install into (default "+install.DefaultNamespace+")")
	f.StringVar(&installOpts.Image, "image", "", "Server image, tag included (default "+install.DefaultImageRepo+":"+install.DefaultImageVersion+")")

	f.StringVar(&installOpts.Database, "database", "", "internal (bundled Postgres) or external (default internal)")
	f.StringVar(&installOpts.DatabaseURL, "database-url", "", "Connection string, with --database=external")

	f.IntVar(&installOpts.APINodePort, "node-port", 0, fmt.Sprintf("Host port the API answers on (default %d)", install.DefaultAPINodePort))
	f.IntVar(&installOpts.OCINodePort, "oci-node-port", 0, fmt.Sprintf("Host port the OCI registry answers on (default %d)", install.DefaultOCINodePort))

	f.StringVar(&installOpts.AdminKeyName, "admin-key-name", "", "Name for the administrator key in cub's key directory (default "+install.DefaultAdminKeyName+")")
	f.StringVar(&installOpts.OutDir, "out-dir", "", "Where to write the generated manifests (default ~/.confighub/servers/<name>)")

	f.BoolVar(&installOpts.DryRun, "dry-run", false, "Generate everything and create nothing")
	f.BoolVar(&installOpts.SkipAuth, "skip-auth", false, "Install without signing in")

	rootCmd.AddCommand(installCmd)
}
