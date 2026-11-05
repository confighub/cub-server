package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/config"
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

Re-running is safe. Generated values -- the token signing key, the database
password -- are read back from the previous run's output and reused, because
rotating them would log every session out and lock the server out of its
database.

Examples:
  cub server install -i                        answer questions, then install
  cub server install                           defaults: a kind cluster
  cub server install --node-port 32200         choose the host port yourself
  cub server install --kube-context my-cluster a cluster you already have
  cub server install --dry-run                 render the manifests, create nothing
  cub server install --installer               render them with the installer package
  cub server install --oci-url <url>           advertise the OCI registry at <url>
  cub server install --storage-class <name>    put the database's volume on <name>
  cub server install --existing-secret <name>  read secrets from a Secret you provide
  cub server install --namespace-label k=v     label the namespace, as admission policies may require
  cub server install --create-namespace=false  deploy into a namespace you created

With --installer, the manifests are rendered by ConfigHub's installer, a cub
plugin (cub plugin install confighub/installer; 0.3.4 or later), from
ConfigHub's installer package, rather than from this plugin's own manifests.
The package is pulled from ghcr.io/confighub/packages/confighub, or from
<prefix>/packages/confighub of a mirror --registry names, at the newest version
not newer than the server's; --installer-package names another. Everything
else is the same: this command still generates the keys and the Secrets, which
the package leaves out, applies the result, and signs in. An instance keeps the
renderer it was installed with; pass --installer=false to go back.

On a cluster whose admission policies require labels on a namespace,
--namespace-label and --namespace-annotation set them on the Namespace the
installer renders, with cub function local. --create-namespace=false instead
deploys into a namespace you created, and never applies or deletes it. Each
implies --installer, and is kept by later runs.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		installOpts.InstallerSet = cmd.Flags().Changed("installer")
		installOpts.ExistingSecretSet = cmd.Flags().Changed("existing-secret")
		if cmd.Flags().Changed("create-namespace") {
			installOpts.CreateNamespace = &installCreateNamespace
		}
		if cmd.Flags().Changed("namespace-label") {
			labels, err := install.ParseNamespaceLabels(installNamespaceLabels)
			if err != nil {
				return err
			}
			installOpts.NamespaceLabels, installOpts.NamespaceLabelsSet = labels, true
		}
		if cmd.Flags().Changed("namespace-annotation") {
			annotations, err := install.ParseNamespaceAnnotations(installNamespaceAnnotations)
			if err != nil {
				return err
			}
			installOpts.NamespaceAnnotations, installOpts.NamespaceAnnotationsSet = annotations, true
		}
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

var (
	installInteractive          bool
	installCreateNamespace      bool
	installNamespaceLabels      []string
	installNamespaceAnnotations []string
)

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
	f.BoolVar(&installCreateNamespace, "create-namespace", true, "Create the namespace; false deploys into one you created, which install leaves as it is, and implies --installer (default: what a previous run recorded, else true)")
	f.StringArrayVar(&installNamespaceLabels, "namespace-label", nil, "Label to set on the namespace install creates, as key=value, for clusters whose admission policies require them; repeatable, and \"\" clears them. Implies --installer (default: the ones a previous run recorded)")
	f.StringArrayVar(&installNamespaceAnnotations, "namespace-annotation", nil, "Annotation to set on the namespace install creates, as key=value; repeatable, and \"\" clears them. Implies --installer (default: the ones a previous run recorded)")
	f.StringVar(&installOpts.Registry, "registry", "", "Where to pull ConfigHub's images from: ghcr, gcr, or a mirror prefix holding <prefix>/confighub and <prefix>/ui, and <prefix>/packages/confighub for --installer (default ghcr)")
	f.StringVar(&installOpts.Image, "image", "", "Server image, tag included (default: the newest released version, resolved from the registry)")
	f.StringVar(&installOpts.UIImage, "ui-image", "", "Web UI image, tag included (default: the UI release matching the server's version)")

	f.StringVar(&installOpts.Database, "database", "", "internal (bundled Postgres) or external (default internal)")
	f.StringVar(&installOpts.DatabaseURL, "database-url", "", "Connection string, with --database=external")
	f.StringVar(&installOpts.StorageClass, "storage-class", "", "StorageClass the bundled database's volume claim names; needed on a cluster with no default StorageClass (default: the one a previous run recorded, else the cluster's default)")
	f.StringVar(&installOpts.ExistingSecret, "existing-secret", "", "Secret the server reads its secrets from, which you provide (with External Secrets or the Secrets Store CSI driver) and install never applies; the values install generates are written to the out-dir for your secret store (default: the one a previous run recorded, else install applies "+config.DefaultSecretName+" itself)")
	f.StringVar(&installOpts.StorageSize, "storage-size", "", "Size of the bundled database's volume, such as 20Gi (default: the one a previous run recorded, else "+config.DefaultStorageSize+")")

	f.IntVar(&installOpts.APINodePort, "node-port", 0, fmt.Sprintf("Host port the API answers on (default %d)", install.DefaultAPINodePort))
	f.IntVar(&installOpts.UINodePort, "ui-node-port", 0, fmt.Sprintf("Host port the web UI answers on (default %d)", install.DefaultUINodePort))
	f.IntVar(&installOpts.OCINodePort, "oci-node-port", 0, fmt.Sprintf("Host port the OCI registry answers on (default %d)", install.DefaultOCINodePort))
	f.StringVar(&installOpts.OCIURL, "oci-url", "", "URL clients reach the OCI registry at, such as https://oci.example.com, written to OCI_EXTERNAL_URL (default: the one a previous run recorded; unset, the server derives the scheme, host and port from the client's request)")
	// Published now even though nothing is deployed behind it, because kind can
	// only publish a port when it creates the node. See internal/install/kind.go.
	f.IntVar(&installOpts.KeycloakNodePort, "keycloak-node-port", 0,
		fmt.Sprintf("Host port to reserve for 'cub server keycloak install' (default %d)", install.DefaultKeycloakNodePort))

	f.StringVar(&installOpts.AdminKeyName, "admin-key-name", "", "Name for the administrator key in cub's key directory (default "+install.DefaultAdminKeyName+")")
	f.BoolVar(&installOpts.NewAdminKey, "new-admin-key", false, "Generate a new administrator keypair instead of reusing one already in cub's key store")
	f.StringVar(&installOpts.OutDir, "out-dir", "", "Where to write the generated manifests (default ~/.confighub/servers/<name>)")

	f.BoolVar(&installOpts.DryRun, "dry-run", false, "Generate everything and create nothing")
	f.BoolVar(&installOpts.SkipAuth, "skip-auth", false, "Install without signing in")
	f.BoolVar(&installOpts.Installer, "installer", false, "Render the manifests with the installer, from ConfigHub's installer package (default: the renderer this instance was installed with)")
	f.StringVar(&installOpts.InstallerPackage, "installer-package", "", "Installer package to render, implying --installer: an oci:// reference, a directory or a .tgz (default: the package for the server's version, from ghcr.io or the --registry mirror)")

	rootCmd.AddCommand(installCmd)
}
