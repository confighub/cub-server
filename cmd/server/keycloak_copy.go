package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var keycloakCopyOpts install.CopyOptions

var keycloakCopyCmd = &cobra.Command{
	Use:   "copy-local",
	Short: "Copy the Local organization's Spaces into the organization Keycloak users are in",
	Long: `Copy what the administrator built before Keycloak was installed into the
organization people who sign in through Keycloak are in.

Everything made with the administrator key belongs to the Local organization,
and Keycloak users are in an organization of their own, so they do not see it.
The server cannot move a Space between organizations; this copies one. It
reads the Local organization with the administrator key the instance trusts
and writes as a cub context logged in to the Keycloak organization, so first:

  1. add yourself in the Keycloak console ('cub server keycloak open') as a
     member of the organization, with the admin role
  2. log in as that user, in a context of its own:
       cub context create keycloak --server=<the instance's API URL> --use
       cub auth login
     (a plain 'cub auth login' in the administrator's context signs in with
     the administrator key again, rather than through Keycloak)
  3. cub server keycloak copy-local --dry-run, to see what it would do

The context is the only one on this server that is not the administrator's,
or the one named by --to-context.

Spaces are copied with their labels and annotations, Units with their current
data, and the Links between them. Not copied: revision history, targets and
workers (a target is a cluster's credentials in the organization that
registered it), triggers, filters and views. Nothing is deleted from Local.
What only the administrator could read becomes readable by every member of
the Keycloak organization who can view the Space it lands in, so check the
--dry-run for anything that should stay in Local.

Links are copied with all their settings, between the copies of their Units.
A Link to a Unit in a Space not being copied, or one that runs an Invocation,
is left out and counted.

A Space that is already there by slug -- the default Space, usually -- gets
the Units it does not have; a Unit that is already there is left as it is. So
re-running it copies only what is new.

Examples:
  cub server keycloak copy-local --dry-run           show what would be copied
  cub server keycloak copy-local                     copy every Space
  cub server keycloak copy-local --space apps        copy one Space
  cub server keycloak copy-local --to-context alice  write as this cub context`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.RunCopyLocal(cmd.Context(), ui, &keycloakCopyOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nCopy failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	o := &keycloakCopyOpts
	f := keycloakCopyCmd.Flags()
	f.StringVar(&o.ToContext, "to-context", "", "cub context to write as, logged in to the Keycloak organization (default: the only other context on this server)")
	f.StringSliceVar(&o.Spaces, "space", nil, "Space to copy, by slug; repeatable (default: every Space)")
	f.BoolVar(&o.DryRun, "dry-run", false, "Say what would be copied without writing anything")

	// The flags that locate the instance, matching `keycloak install`.
	f.StringVar(&o.Instance.OutDir, "out-dir", "", "Directory holding the instance's generated manifests")
	f.StringVar(&o.Instance.Namespace, "namespace", "", "Namespace the instance is in")
	f.StringVar((*string)(&o.Instance.Target), "target", "", "")
	_ = f.MarkHidden("target")
	f.StringVar(&o.Instance.ClusterName, "cluster-name", "", "kind cluster the instance is in")
	f.StringVar(&o.Instance.KubeContext, "kube-context", "", "Kubeconfig context the instance is in")

	keycloakCmd.AddCommand(keycloakCopyCmd)
}
