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
running Docker. Name a kubeconfig context with --kube-context and it installs
into a cluster you already have, using the same manifests.

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
  cub server install --node-port 32200         choose the host port yourself
  cub server install --kube-context my-cluster a cluster you already have
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

	// Superseded by --kube-context, which says the same thing by naming the
	// cluster. Accepted so anything written against v0.2.0 keeps working.
	f.StringVar((*string)(&installOpts.Target), "target", "", "")
	_ = f.MarkHidden("target")
	f.StringVar(&installOpts.ClusterName, "cluster-name", "", "Name of the kind cluster to create (default "+install.DefaultClusterName+")")
	f.StringVar(&installOpts.KubeContext, "kube-context", "", "Kubeconfig context to install into, instead of creating a kind cluster")

	f.StringVar(&installOpts.Namespace, "namespace", "", "Namespace to install into (default "+install.DefaultNamespace+")")
	f.StringVar(&installOpts.Image, "image", "", "Server image, tag included (default: the newest released version, resolved from the registry)")
	f.StringVar(&installOpts.UIImage, "ui-image", "", "Web UI image, tag included (default: the UI release matching the server's version)")

	f.StringVar(&installOpts.Database, "database", "", "internal (bundled Postgres) or external (default internal)")
	f.StringVar(&installOpts.DatabaseURL, "database-url", "", "Connection string, with --database=external")

	f.IntVar(&installOpts.APINodePort, "node-port", 0, fmt.Sprintf("Host port the API answers on (default %d)", install.DefaultAPINodePort))
	f.IntVar(&installOpts.UINodePort, "ui-node-port", 0, fmt.Sprintf("Host port the web UI answers on (default %d)", install.DefaultUINodePort))
	f.IntVar(&installOpts.OCINodePort, "oci-node-port", 0, fmt.Sprintf("Host port the OCI registry answers on (default %d)", install.DefaultOCINodePort))
	// Published now even though nothing is deployed behind it, because kind can
	// only publish a port when it creates the node. See internal/install/kind.go.
	f.IntVar(&installOpts.KeycloakNodePort, "keycloak-node-port", 0,
		fmt.Sprintf("Host port to reserve for 'cub server keycloak install' (default %d)", install.DefaultKeycloakNodePort))

	f.StringVar(&installOpts.AdminKeyName, "admin-key-name", "", "Name for the administrator key in cub's key directory (default "+install.DefaultAdminKeyName+")")
	f.BoolVar(&installOpts.NewAdminKey, "new-admin-key", false, "Generate a new administrator keypair instead of reusing one already in cub's key store")
	f.StringVar(&installOpts.OutDir, "out-dir", "", "Where to write the generated manifests (default ~/.confighub/servers/<name>)")

	f.BoolVar(&installOpts.DryRun, "dry-run", false, "Generate everything and create nothing")
	f.BoolVar(&installOpts.SkipAuth, "skip-auth", false, "Install without signing in")

	rootCmd.AddCommand(installCmd)
}
