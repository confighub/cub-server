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

Run 'keycloak bootstrap' first: it creates the realm and the server's client,
and is the one step that needs an administrator of the whole Keycloak. This
signs in as the server's client, with the key bootstrap wrote, so it can reach
this realm and nothing else. It creates or updates, as needed:

  organizations, turned on for the realm
  the CLI's client and the UI's client, returning only to --ui-url
  the organization users have to be members of
  the realm roles the server reads org roles from:
    admin, manager, editor, user, viewer, creator, none

It then writes merge patches
for the server's and the UI's ConfigMaps, and for the server's key either a
Secret patch or, when the Secret is produced from a secret store, the key as a
file to put in the store plus what to add to the object that produces the
Secret: an ExternalSecret (--secrets external-secrets) or a SecretProviderClass
and the Deployment's volume (--secrets secrets-store-csi). Merge them into your manifests, or apply them with the kubectl
commands it prints. Nothing here touches a cluster.

Re-running it is safe: the realm is reconciled. The server's key is read from
--out-dir, where bootstrap writes it, or from --client-key-file.

Examples:
  cub server keycloak configure \
    --keycloak-url https://keycloak.example.com \
    --ui-url https://confighub.example.com \
    --org example --org-domain example.com

  with the server's Secret produced by External Secrets:
  cub server keycloak configure ... \
    --secrets external-secrets --remote-key confighub/server --remote-property KEYCLOAK_CLIENT_PRIVATE_KEY_JWK

  with the server's Secret synced by the Secrets Store CSI driver from AWS Secrets Manager:
  cub server keycloak configure ... \
    --secrets secrets-store-csi --secret-provider-class confighub \
    --remote-key confighub/server --remote-property KEYCLOAK_CLIENT_PRIVATE_KEY_JWK`,
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
	f.StringVar(&o.AdminURL, "admin-url", "", "Where this command reaches Keycloak, if not --keycloak-url")
	f.StringVar(&o.InternalURL, "internal-url", "", "Where the server reaches Keycloak, if not --keycloak-url")

	f.StringVar(&o.Keycloak.Realm, "realm", "", "Realm to set up, as bootstrapped (default "+config.DefaultKeycloakRealm+")")
	f.StringVar(&o.Keycloak.Org, "org", "", "Organization users belong to, if the realm has none yet (default "+config.DefaultKeycloakOrg+")")
	f.StringVar(&o.Keycloak.OrgDomain, "org-domain", "",
		"Email domain of that organization (default "+config.DefaultKeycloakOrgDomain+")")
	f.StringVar(&o.Keycloak.ClientID, "client-id", "", "Client the server authenticates as (default "+config.DefaultKeycloakClientID+")")
	f.StringVar(&o.Keycloak.UIClientID, "ui-client-id", "", "Client the UI signs in as (default "+config.DefaultKeycloakUIClientID+")")

	f.StringVar(&o.Namespace, "namespace", "", "Namespace the instance runs in, for the kubectl commands printed (default "+install.DefaultNamespace+")")
	f.StringVar(&o.OutDir, "out-dir", "", "Where to write the patches and keep the server's key (default ~/.confighub/keycloak/<keycloak host>/<realm>)")
	f.StringVar(&o.ClientKeyFile, "client-key-file", "", "The server's key, as bootstrap wrote it, if not in --out-dir")

	f.StringVar(&o.Secrets, "secrets", "", "How the server's Secret is produced: kubernetes (default), external-secrets for an ExternalSecret, or secrets-store-csi for the Secrets Store CSI driver with AWS Secrets Manager")
	f.StringVar(&o.SecretProviderClass, "secret-provider-class", "", "With --secrets secrets-store-csi: the SecretProviderClass the server's Secret is synced through")
	f.StringVar(&o.ExternalSecret, "external-secret", "", "With --secrets external-secrets: the ExternalSecret producing the server's Secret (default confighub-secret-env)")
	f.StringVar(&o.RemoteKey, "remote-key", "", "With --secrets external-secrets or secrets-store-csi: the secret-store entry the server's key is kept in")
	f.StringVar(&o.RemoteProperty, "remote-property", "", "With --secrets external-secrets or secrets-store-csi: the property within --remote-key, if it holds several values")

	keycloakCmd.AddCommand(keycloakConfigureCmd)
}
