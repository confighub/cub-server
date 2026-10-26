package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/config"
	"github.com/confighub/cub-server/internal/install"
)

var keycloakConfigureOpts install.ConfigureOptions

var keycloakConfigureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Set up a Keycloak you run yourself for an instance you deployed yourself",
	Long: `Set up a realm for ConfigHub on a Keycloak you run, and write out the
server's half for you to apply.

For an instance deployed from manifests you manage, with a Keycloak you run for
it. 'keycloak install' is for the other case: it deploys Keycloak next to an
instance this plugin installed, and applies everything itself.

This signs in to Keycloak's admin API as a master-realm administrator and
creates or updates, as needed:

  the realm, with organizations turned on
  the server's client, which authenticates with a key generated here
  the CLI's client and the UI's client, returning only to --ui-url
  the organization users have to be members of
  the realm roles the server reads org roles from:
    admin, manager, editor, user, viewer, creator, none

It then checks that Keycloak accepts the server's key, and writes merge patches
for the server's and the UI's ConfigMaps, and for the server's key either a
Secret patch or, with --secrets external-secrets, an entry for the
ExternalSecret that produces that Secret plus the key as a file to put in your
secret store. Merge them into your manifests, or apply them with the kubectl
commands it prints. Nothing here touches a cluster.

Re-running it is safe. The realm is reconciled, and the server's key is read
back from --out-dir, or from --client-key-file. Without either, a new key is
made and Keycloak is switched to it.

The admin password is read from $KEYCLOAK_ADMIN_PASSWORD, or asked for. A
master-realm client with the admin role can be used instead: --admin-client-id,
with its secret in $KEYCLOAK_ADMIN_CLIENT_SECRET.

Examples:
  cub server keycloak configure \
    --keycloak-url https://keycloak.example.com \
    --ui-url https://confighub.example.com \
    --org example --org-domain example.com

  with the server's Secret produced by External Secrets:
  cub server keycloak configure ... \
    --secrets external-secrets --remote-key confighub/server --remote-property KEYCLOAK_CLIENT_PRIVATE_KEY_JWK`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.RunConfigure(cmd.Context(), ui, os.Stdin, &keycloakConfigureOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nKeycloak configure failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	o := &keycloakConfigureOpts
	f := keycloakConfigureCmd.Flags()
	f.StringVar(&o.Keycloak.PublicURL, "keycloak-url", "", "Where browsers reach Keycloak. It is the issuer of every token, so it must be the address people use")
	f.StringVar(&o.UIURL, "ui-url", "", "Where browsers reach the ConfigHub UI")
	f.StringVar(&o.AdminURL, "admin-url", "", "Where this command reaches Keycloak's admin API, if not --keycloak-url")
	f.StringVar(&o.InternalURL, "internal-url", "", "Where the server reaches Keycloak, if not --keycloak-url")

	f.StringVar(&o.Admin.User, "admin-user", "", "Master-realm admin user (default admin); password in $KEYCLOAK_ADMIN_PASSWORD")
	f.StringVar(&o.Admin.ClientID, "admin-client-id", "", "Master-realm admin client to use instead of a user; secret in $KEYCLOAK_ADMIN_CLIENT_SECRET")

	f.StringVar(&o.Keycloak.Realm, "realm", "", "Realm to create or update (default "+config.DefaultKeycloakRealm+")")
	f.StringVar(&o.Keycloak.Org, "org", "", "Organization users belong to, if the realm has none yet (default "+config.DefaultKeycloakOrg+")")
	f.StringVar(&o.Keycloak.OrgDomain, "org-domain", "",
		"Email domain of that organization (default "+config.DefaultKeycloakOrgDomain+")")
	f.StringVar(&o.Keycloak.ClientID, "client-id", "", "Client the server authenticates as (default "+config.DefaultKeycloakClientID+")")
	f.StringVar(&o.Keycloak.UIClientID, "ui-client-id", "", "Client the UI signs in as (default "+config.DefaultKeycloakUIClientID+")")

	f.StringVar(&o.Namespace, "namespace", "", "Namespace the instance runs in, for the kubectl commands printed (default "+install.DefaultNamespace+")")
	f.StringVar(&o.OutDir, "out-dir", "", "Where to write the patches and keep the server's key (default ~/.confighub/keycloak/<keycloak host>/<realm>)")
	f.StringVar(&o.ClientKeyFile, "client-key-file", "", "The server's key from an earlier run, if not kept in --out-dir")

	f.StringVar(&o.Secrets, "secrets", "", "How the server's Secret is produced: kubernetes (default), or external-secrets for an ExternalSecret")
	f.StringVar(&o.ExternalSecret, "external-secret", "", "With --secrets external-secrets: the ExternalSecret producing the server's Secret (default confighub-secret-env)")
	f.StringVar(&o.RemoteKey, "remote-key", "", "With --secrets external-secrets: where in the secret store the server's key is kept")
	f.StringVar(&o.RemoteProperty, "remote-property", "", "With --secrets external-secrets: the property within --remote-key, if it holds several values")

	keycloakCmd.AddCommand(keycloakConfigureCmd)
}
