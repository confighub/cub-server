package main

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use: "server",

	// A command that fails while installing has already explained itself; the
	// flag list underneath would bury that explanation and implies the problem
	// was how the command was typed, which by then it usually is not. Usage is
	// still printed for an actual usage error, which cobra reports before RunE.
	SilenceUsage:  true,
	SilenceErrors: true,

	Short: "Install and manage a self-hosted ConfigHub server",
	Long: `server stands up a ConfigHub instance you own — the server (API/UI and OCI
endpoints) and a database — and leaves you logged into it. Run it as
'cub server <command>'.

It installs into Kubernetes, because that is where ConfigHub runs in earnest.
If you have no cluster it creates a local one with kind; if you have one it
installs into that. Both use the same manifests, so an evaluation on a laptop
and a real deployment differ in where they run, not in what runs.

No identity provider is required. The instance is created with a local
administrator whose keypair is generated during the install, so there is no
Keycloak to stand up, no realm to configure, and nobody to ask for a redirect
URI. 'cub server install' ends with you authenticated.

PREREQUISITES
  A running Docker, for the kind target. That is all when kind creates the
  cluster; installing into an existing cluster needs only a kubeconfig.

GETTING STARTED
  cub server install -i        answer a few questions, then it installs
  cub server install           the same thing from flags, for scripts and CI

AFTERWARDS
  cub space list               you are already authenticated
  cub auth browser-session     open the same session in a browser`,
}
