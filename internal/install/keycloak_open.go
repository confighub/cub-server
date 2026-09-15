package install

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/skratchdot/open-golang/open"
	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// Opening Keycloak's admin console with the password in hand.
//
// Modelled on `cub cluster open`, which does the same for Argo CD: resolve the
// URL and the admin password, put the password on the clipboard, launch the
// browser. The password is fetched before the browser is, so the paste is ready
// by the time the login form is.
//
// Copying rather than printing is the point. A password echoed to the terminal
// is in the scrollback, in the shell history of whoever pipes it, and in any log
// of the session; one on the clipboard is pasted once. --print-password is there
// for the cases where the clipboard is not (containers, ssh, CI), and the
// command falls back to printing on its own when copying fails, because a
// password nobody can reach is worse than one on screen.

// OpenKeycloak launches the browser on the instance's Keycloak admin console.
func OpenKeycloak(ctx context.Context, out io.Writer, o *Options, printURL, printPassword bool) error {
	if err := o.Defaults(); err != nil {
		return err
	}
	if err := requireKeycloakInstalled(o); err != nil {
		return err
	}

	url, err := keycloakConsoleURL(o)
	if err != nil {
		return err
	}

	if printURL {
		fmt.Fprintln(out, url)
		return nil
	}

	// Before the browser, so the paste is ready by the time the form is.
	password, passwordErr := keycloakAdminPassword(ctx, o)

	if err := open.Start(url); err != nil {
		return fmt.Errorf("failed to open browser on %s: %w", url, err)
	}
	fmt.Fprintf(out, "Opened the Keycloak admin console: %s\n", url)
	fmt.Fprintf(out, "  user:     %s\n", config.DefaultKeycloakAdminUser)
	switch {
	case password == "":
		fmt.Fprintf(out, "  password: could not be determined (%v)\n", passwordErr)
	case printPassword:
		fmt.Fprintf(out, "  password: %s\n", password)
	default:
		if cerr := clipboardCopy(ctx, password); cerr != nil {
			fmt.Fprintf(out, "  password: %s  (could not copy to clipboard: %v)\n", password, cerr)
		} else {
			fmt.Fprintf(out, "  password: in your clipboard — paste it into Keycloak's login form\n")
		}
	}
	return nil
}

// requireKeycloakInstalled refuses to open a console that does not exist.
func requireKeycloakInstalled(o *Options) error {
	if _, err := os.Stat(keycloakManifest(o.OutDir)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"%s has no identity provider to open.\n\n"+
					"    To install one:\n"+
					"      cub server keycloak install%s",
				o.OutDir, outDirFlag(o))
		}
		return err
	}
	return nil
}

// keycloakSetting reads one of this instance's generated ConfigMap values.
//
// The generated manifests are the record of what was installed, so they are
// where any command asks what this instance's Keycloak looks like rather than
// re-deriving it from flags and defaults that may not match.
func keycloakSetting(o *Options, key string) (string, error) {
	path := filepath.Join(o.OutDir, config.ConfigDir, "20-configmap.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	value := doc.Data[key]
	if value == "" {
		return "", fmt.Errorf("no %s in %s", key, path)
	}
	return value, nil
}

// keycloakBaseURL is the browser-facing address of this instance's Keycloak,
// which is also the one every command here dials: it is reachable from wherever
// the operator is running, and the in-cluster address is not.
func keycloakBaseURL(o *Options) (string, error) {
	base, err := keycloakSetting(o, "KEYCLOAK_AUTH_URL")
	if err != nil {
		return "", fmt.Errorf("%w, so this instance's Keycloak address is unknown", err)
	}
	return strings.TrimSuffix(base, "/"), nil
}

func keycloakRealmName(o *Options) (string, error) {
	return keycloakSetting(o, "KEYCLOAK_REALM")
}

// keycloakConsoleURL is the admin console of the instance's Keycloak.
func keycloakConsoleURL(o *Options) (string, error) {
	base, err := keycloakBaseURL(o)
	if err != nil {
		return "", err
	}
	return base + "/admin", nil
}

// keycloakAdminPassword resolves Keycloak's admin password, preferring the
// generated Secret and falling back to the live cluster.
//
// The local file is the no-round-trip source and the usual one. The fallback
// covers an instance whose generated files were moved or discarded -- they are a
// record, not the instance -- which is the same shape `cub cluster open` uses
// when its env file is gone.
//
// An empty password with a non-nil error is an expected outcome the caller
// reports rather than fails on: the console is still worth opening, and someone
// who has rotated the password does not need this command to tell them what it
// used to be.
func keycloakAdminPassword(ctx context.Context, o *Options) (string, error) {
	const key = "KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD"

	path := filepath.Join(o.OutDir, config.SecretsDir, "confighub-keycloak-secret.yaml")
	if content, err := os.ReadFile(path); err == nil {
		var doc struct {
			StringData map[string]string `yaml:"stringData"`
		}
		if yaml.Unmarshal(content, &doc) == nil && doc.StringData[key] != "" {
			return doc.StringData[key], nil
		}
	}

	kube, err := openKubeEnv(o)
	if err != nil {
		return "", fmt.Errorf("no password in %s and no way to reach the cluster: %w", path, err)
	}
	encoded, err := run(ctx, toolKubectl.name, kube.args(
		"get", "secret", "confighub-keycloak-secret",
		"-n", o.Namespace,
		"-o", "jsonpath={.data."+key+"}",
	)...)
	if err != nil {
		return "", fmt.Errorf("no password in %s and could not read it from the cluster: %w", path, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", fmt.Errorf("the %s secret in the cluster is not valid base64: %w", key, err)
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("no password in %s and none in the cluster's secret either", path)
	}
	return string(decoded), nil
}

// openKubeEnv points kubectl at the instance without creating anything.
//
// provisionCluster would create a kind cluster that is missing, which is right
// for an install and wrong for a command that only reads: opening a console for
// an instance that is not there should say so, not build one.
func openKubeEnv(o *Options) (kubeEnv, error) {
	if o.Target == TargetContext {
		return kubeEnv{context: o.KubeContext}, nil
	}
	kubeconfig, err := kubeconfigPath(o.OutDir)
	if err != nil {
		return kubeEnv{}, err
	}
	if _, err := os.Stat(kubeconfig); err != nil {
		return kubeEnv{}, fmt.Errorf("no kubeconfig at %s", kubeconfig)
	}
	return kubeEnv{kubeconfig: kubeconfig}, nil
}
