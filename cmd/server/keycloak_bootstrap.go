package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/config"
	"github.com/confighub/cub-server/internal/install"
)

var keycloakBootstrapOpts install.BootstrapOptions

var keycloakBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Create ConfigHub's realm on a Keycloak you run (needs a Keycloak administrator)",
	Long: `Create ConfigHub's realm and the server's client on a Keycloak you run.

This is the one step that needs an administrator of the whole Keycloak: a
master-realm admin user, with the password in $KEYCLOAK_ADMIN_PASSWORD (or
asked for), or a master-realm client with the admin role, --admin-client-id
with its secret in $KEYCLOAK_ADMIN_CLIENT_SECRET.

It creates the realm, if it is not there, with one client: the server's. That
client signs in with a key generated here, of which Keycloak gets only the
public half, and its service account administers this realm and nothing else
on this Keycloak. The private key is written to --out-dir.

That key is the server's credential, and all that 'keycloak configure' and
'keycloak idp' need to set up the rest. So whoever administers Keycloak can run
this once and hand the key to whoever runs ConfigHub.

Run on a realm that exists, it sets the server's client to the key (the one in
--out-dir, --client-key-file, or a new one) and grants its service account the
realm's administration again.

Examples:
  cub server keycloak bootstrap --keycloak-url https://keycloak.example.com`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.RunBootstrap(cmd.Context(), ui, os.Stdin, &keycloakBootstrapOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nKeycloak bootstrap failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	o := &keycloakBootstrapOpts
	f := keycloakBootstrapCmd.Flags()
	f.StringVar(&o.Keycloak.PublicURL, "keycloak-url", "", "Where browsers reach Keycloak. It is the issuer of every token, so it must be the address people use")
	f.StringVar(&o.AdminURL, "admin-url", "", "Where this command reaches Keycloak, if not --keycloak-url")
	f.StringVar(&o.Admin.User, "admin-user", "", "Master-realm admin user (default admin); password in $KEYCLOAK_ADMIN_PASSWORD")
	f.StringVar(&o.Admin.ClientID, "admin-client-id", "", "Master-realm admin client to use instead of a user; secret in $KEYCLOAK_ADMIN_CLIENT_SECRET")
	f.StringVar(&o.Keycloak.Realm, "realm", "", "Realm to create (default "+config.DefaultKeycloakRealm+")")
	f.StringVar(&o.Keycloak.ClientID, "client-id", "", "Client the server authenticates as (default "+config.DefaultKeycloakClientID+")")
	f.StringVar(&o.OutDir, "out-dir", "", "Where to write the server's key (default ~/.confighub/keycloak/<keycloak host>/<realm>)")
	f.StringVar(&o.ClientKeyFile, "client-key-file", "", "A server key to use instead of the one in --out-dir or a new one")
	keycloakCmd.AddCommand(keycloakBootstrapCmd)
}
