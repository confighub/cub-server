package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/config"
	"github.com/confighub/cub-server/internal/install"
)

// The three credentials a ConfigHub instance needs before it starts, each
// available on its own.
//
// `cub server install` generates all three and never asks. These exist for the
// cases that are not a fresh install: bringing your own configuration, rotating
// one value, or generating in one place and deploying from another.
//
// Each writes one value to stdout and everything else to stderr, so the value
// can be piped into a secret manager rather than read off a screen.

var keyCmd = &cobra.Command{
	Use:   "key",
	Short: "Generate the credentials a ConfigHub instance needs",
	Long: `Generate the credentials a ConfigHub instance needs before it starts.

'cub server install' generates all of these itself. Use these when you are
managing the instance's configuration yourself, or rotating one value.

Each writes one value to stdout and nothing else, so it can be piped:

  cub server key worker-secret | kubectl create secret generic ... --from-file=-`,
}

var keyAdminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Generate the local administrator's keypair",
	Long: `Generate the keypair for the local administrator of an instance with no identity
provider.

The two halves have opposite destinations and are handled separately. The public
half goes to stdout and belongs in the instance's configuration as
` + config.AdminJWKEnv + ` -- it is not a secret, which is the point of storing
public keys rather than passwords. The private half is written to cub's key
directory, where 'cub auth login --private-key' finds it by name, and must never
reach the cluster.

Run this before the server starts: it needs the public key to have an
administrator at all, so there is no way to obtain one through the API first.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		pair, err := config.GenerateAdminKeypair()
		if err != nil {
			return err
		}

		store, err := install.CubStore()
		if err != nil {
			return err
		}
		path, err := store.WritePrivateKey(keyAdminName, json.RawMessage(pair.PrivateJWK))
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "Private key written to %s\n", path)
		fmt.Fprintf(os.Stderr, "Sign in with: cub auth login --private-key=%s --server=<url>\n\n", keyAdminName)
		fmt.Fprintf(os.Stderr, "Set this in the instance's configuration as %s:\n", config.AdminJWKEnv)
		fmt.Println(pair.PublicJWK)
		return nil
	},
}

var keySigningCmd = &cobra.Command{
	Use:   "signing",
	Short: "Generate the token signing key (JWT_PRIVATE_KEY_JWK)",
	Long: `Generate the RSA key the server signs every ConfigHub token with.

The server treats this as optional, and that is the trap: unset, it mints a
fresh one at every start, so every restart logs everyone out and two replicas
reject each other's tokens. Generating it once and keeping it is what makes the
default behaviour the correct one.

Rotating it invalidates every session.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		key, err := config.GenerateSigningKey()
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	},
}

var keyWorkerCmd = &cobra.Command{
	Use:   "worker-secret",
	Short: "Generate the worker master secret (WORKER_MASTER_SECRET)",
	Long: `Generate the secret keying the HMAC over worker credentials.

Changing it invalidates every worker that has already enrolled, so generate it
once per instance and keep it.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		secret, err := config.RandomSecret()
		if err != nil {
			return err
		}
		fmt.Println(secret)
		return nil
	},
}

var keyAdminName string

func init() {
	keyAdminCmd.Flags().StringVar(&keyAdminName, "name", install.DefaultAdminKeyName,
		"Name for the private key in cub's key directory")

	keyCmd.AddCommand(keyAdminCmd, keySigningCmd, keyWorkerCmd)
	rootCmd.AddCommand(keyCmd)
}
