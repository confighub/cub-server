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
what to register at your provider's end.

OpenID Connect: register an application for ConfigHub at your provider and pass
its discovery URL, client id and secret. Keycloak reads the rest from the
discovery document.

SAML: pass the provider's metadata, as a URL Keycloak can fetch or as an
exported file, and the attribute carrying each person's email. Then register
Keycloak at the provider from the service-provider metadata URL this prints.

Org roles: --role-attribute names the SAML attribute or OIDC claim listing a
person's groups, and each --role-map value=role gives the org role for one
value. Roles follow the provider at every sign-in; someone matching none gets
creator. Re-run with a new set of --role-map to change it; roles mapped by an
earlier run and not listed again are removed.

For a Keycloak you run yourself (see 'keycloak bootstrap'), pass --keycloak-url.
This signs in as the server's client, with the key bootstrap wrote, so it needs
no Keycloak administrator. Otherwise this finds the instance's own Keycloak
through its generated manifests, and signs in the same way.

Examples:
  cub server keycloak idp -i                  answer the questions
  cub server keycloak idp \
    --discovery-url https://acme.okta.com/.well-known/openid-configuration \
    --client-id 0oa1b2c3 --client-secret ... \
    --org "Acme Corp" --org-domain acme.com
  cub server keycloak idp --keycloak-url https://keycloak.acme.com \
    --saml-metadata-file pingfederate-metadata.xml --alias pingfederate \
    --email-attribute mail --org-domain acme.com \
    --role-attribute memberOf --role-map "CN=confighub-admins,OU=Groups,DC=acme,DC=com=admin"`,
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
	f.StringVar(&keycloakIdPArgs.ClientAuthMethod, "client-auth-method", "", "How Keycloak presents the client secret: client_secret_post (default) or client_secret_basic")
	f.StringVar(&keycloakIdPArgs.Alias, "alias", "", "Short name for the provider; appears in the redirect URI (default: guessed from the discovery or metadata URL)")

	f.StringVar(&keycloakIdPArgs.SAMLMetadataURL, "saml-metadata-url", "", "A SAML provider's metadata URL, fetched by Keycloak")
	f.StringVar(&keycloakIdPArgs.SAMLMetadataFile, "saml-metadata-file", "", "A SAML provider's metadata, exported to a file")
	f.StringVar(&keycloakIdPArgs.EmailAttribute, "email-attribute", "", "SAML attribute carrying the email address; the organization's domain is matched against it")
	f.StringVar(&keycloakIdPArgs.FirstNameAttribute, "first-name-attribute", "", "SAML attribute carrying the first name")
	f.StringVar(&keycloakIdPArgs.LastNameAttribute, "last-name-attribute", "", "SAML attribute carrying the last name")

	f.StringVar(&keycloakIdPArgs.RoleAttribute, "role-attribute", "", "SAML attribute or OIDC claim whose values --role-map maps to org roles")
	f.StringArrayVar(&keycloakIdPArgs.RoleMap, "role-map", nil, "value=role: the org role for one value of --role-attribute (repeatable)")

	f.StringVar(&keycloakIdPArgs.KeycloakURL, "keycloak-url", "", "A Keycloak you run, instead of the instance's own")
	f.StringVar(&keycloakIdPArgs.Realm, "realm", "", "Realm on that Keycloak (default confighub)")
	f.StringVar(&keycloakIdPArgs.ClientKeyFile, "client-key-file", "", "With --keycloak-url: the server's key, as keycloak bootstrap wrote it (default: where bootstrap writes it)")
	f.StringVar(&keycloakIdPArgs.ServerClientID, "server-client-id", "", "With --keycloak-url: the client the server authenticates as (default confighub)")

	// The flags that locate the instance, matching the other keycloak commands.
	f.StringVar(&keycloakIdPOpts.OutDir, "out-dir", "", "Directory holding the instance's generated manifests")
	f.StringVar(&keycloakIdPOpts.Namespace, "namespace", "", "Namespace the instance is in")
	f.StringVar((*string)(&keycloakIdPOpts.Target), "target", "", "")
	_ = f.MarkHidden("target")
	f.StringVar(&keycloakIdPOpts.ClusterName, "cluster-name", "", "kind cluster the instance is in")
	f.StringVar(&keycloakIdPOpts.KubeContext, "kube-context", "", "Kubeconfig context the instance is in")

	keycloakCmd.AddCommand(keycloakIdPCmd)
}
