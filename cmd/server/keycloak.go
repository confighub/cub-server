package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/config"
	"github.com/confighub/cub-server/internal/install"
)

var keycloakCmd = &cobra.Command{
	Use:   "keycloak",
	Short: "Add an identity provider to an installed instance",
	Long: `keycloak gives an instance real user accounts.

'cub server install' leaves you with an instance and one administrator, who
signs in with a key. That is enough to evaluate ConfigHub and enough to run it
alone. This is the next chapter: a Keycloak of its own, so other people can log
in as themselves.

It is additive. Your administrator key keeps working afterwards, in the Local
organization the install created. People who sign in through Keycloak are in an
organization of their own, which that key does not reach. For a way back into it
when Keycloak is what is broken, create a service account with the admin org
role there and keep its key safe: it logs in without Keycloak.

The server authenticates to Keycloak by signing an assertion with a key
generated during this install. Keycloak holds only the public half. No client
secret and no Keycloak admin password reach the server.`,
}

var keycloakOpts install.Options

var keycloakInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Keycloak and point the instance at it",
	Long: `Install Keycloak alongside an instance that already exists.

Keycloak is deployed into the same namespace, a realm is imported with the two
clients ConfigHub needs, and the instance is reconfigured and restarted to use
it. The administrator key from the first install keeps working.

The realm carries an organization, and users have to be members of it: ConfigHub
resolves a user's organization from their token and sends anyone who has none to
a pending-approval page. The command prints where to add people when it is done.

The bundled Keycloak runs in development mode with an embedded database, which
is what lets it come up on a laptop with no certificate and no DNS. An instance
serving real users wants a Keycloak of its own.

Examples:
  cub server keycloak install                   defaults
  cub server keycloak install --realm acme      a realm name of your own
  cub server keycloak install --dry-run         render the manifests, change nothing

An instance installed with --installer is rendered with the installer again,
with the package's keycloak component; --installer and --installer=false choose
otherwise. The installer package names the realm, the organization and the
clients after the defaults, so it refuses --realm, --org and --client-id.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		keycloakOpts.InstallerSet = cmd.Flags().Changed("installer")
		if err := install.RunKeycloak(cmd.Context(), ui, &keycloakOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nKeycloak install failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	keycloakOpts.Keycloak = &config.Keycloak{}

	f := keycloakInstallCmd.Flags()
	f.StringVar(&keycloakOpts.Keycloak.Realm, "realm", "", "Realm to create (default "+config.DefaultKeycloakRealm+")")
	f.StringVar(&keycloakOpts.Keycloak.Org, "org", "", "Organization users belong to (default "+config.DefaultKeycloakOrg+")")
	f.StringVar(&keycloakOpts.Keycloak.OrgDomain, "org-domain", "",
		"Email domain of that organization; users with matching addresses are offered membership (default "+config.DefaultKeycloakOrgDomain+")")
	f.StringVar(&keycloakOpts.Keycloak.ClientID, "client-id", "", "OAuth client the server authenticates as (default "+config.DefaultKeycloakClientID+")")
	f.StringVar(&keycloakOpts.Keycloak.Image, "keycloak-image", "", "Keycloak image (default "+config.DefaultKeycloakImage+")")
	f.IntVar(&keycloakOpts.Keycloak.NodePort, "keycloak-node-port", 0,
		fmt.Sprintf("Host port the browser reaches Keycloak on (default %d)", install.DefaultKeycloakNodePort))
	f.StringVar(&keycloakOpts.Keycloak.PublicURL, "public-url", "",
		"Where a browser reaches Keycloak. Every token is issued by this, so it must be the address people actually use")

	// The same install-wide flags the first chapter took, because this command
	// re-renders the same instance and has to find it the same way.
	f.StringVar(&keycloakOpts.OutDir, "out-dir", "", "Directory holding the instance's generated manifests")
	f.StringVar(&keycloakOpts.Namespace, "namespace", "", "Namespace the instance is in")
	f.StringVar((*string)(&keycloakOpts.Target), "target", "", "")
	_ = f.MarkHidden("target")
	f.StringVar(&keycloakOpts.ClusterName, "cluster-name", "", "kind cluster the instance is in")
	f.StringVar(&keycloakOpts.KubeContext, "kube-context", "", "Kubeconfig context the instance is in")
	f.BoolVar(&keycloakOpts.DryRun, "dry-run", false, "Render the manifests without changing the cluster")
	f.BoolVar(&keycloakOpts.Installer, "installer", false, "Render the manifests with the installer, from ConfigHub's installer package (default: the renderer this instance was installed with)")
	f.StringVar(&keycloakOpts.InstallerPackage, "installer-package", "", "Installer package to render, implying --installer: an oci:// reference, a directory or a .tgz (default: the package for the server's version, from ghcr.io or the mirror the server image comes from)")

	keycloakCmd.AddCommand(keycloakInstallCmd)
	rootCmd.AddCommand(keycloakCmd)
}
