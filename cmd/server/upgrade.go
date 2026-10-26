package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-server/internal/install"
)

var upgradeOpts install.UpgradeOptions

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Move a running instance to a newer release",
	Long: `Move a running instance to a newer ConfigHub release.

The instance is read from the cluster, so this works however it was deployed:
by 'cub server install', or from manifests you keep and apply yourself. Nothing
under ~/.confighub/servers is needed.

It changes the image tag of the server (and the init container that migrates
its database) and of the UI, and keeps each image's registry, so a mirror stays
a mirror. Make sure the new version is in your mirror first.

The change is written as patches, one per Deployment. Without --apply nothing
is changed: merge the patches into the manifests you apply, or run the kubectl
commands it prints. With --apply it patches the running Deployments, server
first, and waits for each rollout. Either way, merge the patches into your
manifests, or the next apply of them undoes the upgrade.

Examples:
  cub server upgrade                                     plan the newest release, change nothing
  cub server upgrade --version v0.9.5 --apply            upgrade the instance now
  cub server upgrade --kube-context prod --namespace confighub --out-dir ./upgrade`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ui := install.UI{Out: os.Stdout}
		if err := install.RunUpgrade(cmd.Context(), ui, &upgradeOpts); err != nil {
			fmt.Fprintf(os.Stderr, "\nUpgrade failed: %v\n", err)
			return install.ErrReported
		}
		return nil
	},
}

func init() {
	f := upgradeCmd.Flags()
	f.StringVar(&upgradeOpts.Version, "version", "", "Release to move to (default: the newest published)")
	f.BoolVar(&upgradeOpts.Apply, "apply", false, "Patch the running Deployments as well as writing the patches")
	f.StringVar(&upgradeOpts.Kubeconfig, "kubeconfig", "", "Kubeconfig file (default: kubectl's)")
	f.StringVar(&upgradeOpts.KubeContext, "kube-context", "", "Kubeconfig context the instance is in (default: the current one)")
	f.StringVar(&upgradeOpts.Namespace, "namespace", "", "Namespace the instance is in (default "+install.DefaultNamespace+")")
	f.StringVar(&upgradeOpts.OutDir, "out-dir", "", "Where to write the patches (default ~/.confighub/upgrades/<context>-<namespace>/<version>)")
	rootCmd.AddCommand(upgradeCmd)
}
