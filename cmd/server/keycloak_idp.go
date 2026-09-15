package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var (
	keycloakIdPOpts  install.Options
	keycloakIdPArgs  install.IdPOptions
	keycloakIdPInter bool
)

var keycloakIdPCmd = &cobra.Command{
	Use:   "idp",
	Short: "Connect a corporate identity provider so people sign in as themselves",
	Long: `Connect the identity provider your company already uses.

Two things have to be true before someone can sign in to ConfigHub with their
work account: Keycloak has to broker to your provider, and the person arriving
through it has to become a member of the organization. ConfigHub resolves a
user's organization from their token and shows anyone who has none a
pending-approval page, so a provider wired up without the second part looks like
it works and then does not.

Keycloak joins the two with an email domain. This command sets the
organization's name and domain, registers the provider, links them, and prints
the redirect URI to register at your provider's end.

Your provider needs an OpenID Connect application registered for ConfigHub --
Keycloak reads the rest from its discovery document, so you supply one URL plus
that application's client id and secret. Okta, Entra ID, Google Workspace and
Auth0 all publish one.

Examples:
  cub server keycloak idp -i                  answer the questions
  cub server keycloak idp \
    --discovery-url https://acme.okta.com/.well-known/openid-configuration \
    --client-id 0oa1b2c3 --client-secret ... \
    --org "Acme Corp" --org-domain acme.com`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		keycloakIdPArgs.Interactive = keycloakIdPInter
		ui := install.UI{Out: os.Stdout}
		if err := install.RunIdP(cmd.Context(), ui, os.Stdin, &keycloakIdPOpts, &keycloakIdPArgs); err != nil {
			fmt.Fprintf(os.Stderr, "\nFailed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	f := keycloakIdPCmd.Flags()
	f.BoolVarP(&keycloakIdPInter, "interactive", "i", false, "Ask for what is needed, starting from what this instance already has")

	f.StringVar(&keycloakIdPArgs.OrgName, "org", "", "Organization name (default: leave it as it is)")
	f.StringVar(&keycloakIdPArgs.OrgDomain, "org-domain", "", "Email domain of that organization; it is what matches people to this provider")
	f.StringVar(&keycloakIdPArgs.DiscoveryURL, "discovery-url", "", "The provider's OpenID discovery document")
	f.StringVar(&keycloakIdPArgs.ClientID, "client-id", "", "Client id of the application registered for ConfigHub at the provider")
	f.StringVar(&keycloakIdPArgs.ClientSecret, "client-secret", "", "Client secret of that application")
	f.StringVar(&keycloakIdPArgs.Alias, "alias", "", "Short name for the provider; appears in the redirect URI (default: guessed from the discovery URL)")

	// The flags that locate the instance, matching the other keycloak commands.
	f.StringVar(&keycloakIdPOpts.OutDir, "out-dir", "", "Directory holding the instance's generated manifests")
	f.StringVar(&keycloakIdPOpts.Namespace, "namespace", "", "Namespace the instance is in")
	f.StringVar((*string)(&keycloakIdPOpts.Target), "target", "", "Where the instance runs: kind or context")
	f.StringVar(&keycloakIdPOpts.ClusterName, "cluster-name", "", "kind cluster the instance is in")
	f.StringVar(&keycloakIdPOpts.KubeContext, "kube-context", "", "Kubeconfig context the instance is in")

	keycloakCmd.AddCommand(keycloakIdPCmd)
}
