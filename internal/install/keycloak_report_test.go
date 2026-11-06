package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// keycloakOutDir is an out-dir holding the Keycloak Secret, with password as
// its admin password.
func keycloakOutDir(t *testing.T, password string) string {
	t.Helper()
	dir := t.TempDir()
	secrets := filepath.Join(dir, config.SecretsDir)
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: confighub-keycloak-secret\nstringData:\n" +
		"  KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD: " + password + "\n"
	if err := os.WriteFile(filepath.Join(secrets, config.KeycloakSecretFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func reportFor(t *testing.T, o *Options) string {
	t.Helper()
	o.Keycloak = &config.Keycloak{
		PublicURL: "http://localhost:32182",
		Realm:     "confighub",
		Org:       "default",
		AdminUser: "kcadmin",
	}
	var out bytes.Buffer
	if err := reportKeycloak(UI{Out: &out}, o); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// The admin password is the master realm's administrator: printed, it lands in
// scrollback and CI logs. The report says how to get it instead.
func TestKeycloakReportDoesNotPrintThePassword(t *testing.T) {
	const password = "s3cret-kcadmin-password"
	dir := keycloakOutDir(t, password)
	got := reportFor(t, &Options{OutDir: dir, ClusterName: "lab", Namespace: DefaultNamespace})

	if strings.Contains(got, password) {
		t.Errorf("the report prints the admin password:\n%s", got)
	}
	for _, want := range []string{
		"cub server keycloak open",
		filepath.Join(dir, config.SecretsDir, config.KeycloakSecretFile),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not mention %q:\n%s", want, got)
		}
	}
}

// A command printed bare acts on the default instance, so for any other the
// report names this one.
func TestKeycloakReportCommandsNameTheInstance(t *testing.T) {
	dir := keycloakOutDir(t, "pw")
	got := reportFor(t, &Options{OutDir: dir, ClusterName: "lab", Namespace: "team a"})

	flags := " --cluster-name=lab --out-dir=" + dir + " --namespace='team a'"
	for _, want := range []string{
		"cub server keycloak open" + flags + "\n",
		"cub server keycloak import-local --dry-run" + flags + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not contain %q:\n%s", want, got)
		}
	}
}

func TestInstanceFlags(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	defaultDir := filepath.Join(home, ".confighub", "servers", DefaultClusterName)
	cases := []struct {
		name string
		o    Options
		want string
	}{
		{"the default instance", Options{Target: TargetKind, ClusterName: DefaultClusterName, OutDir: defaultDir, Namespace: DefaultNamespace}, ""},
		{"another kind cluster, its default out-dir", Options{Target: TargetKind, ClusterName: "lab",
			OutDir: filepath.Join(home, ".confighub", "servers", "lab"), Namespace: DefaultNamespace}, " --cluster-name=lab"},
		{"a kubeconfig context", Options{Target: TargetContext, KubeContext: "prod",
			OutDir: filepath.Join(home, ".confighub", "servers", "prod"), Namespace: DefaultNamespace}, " --kube-context=prod"},
		{"an out-dir of its own", Options{Target: TargetKind, ClusterName: DefaultClusterName, OutDir: "/srv/ch", Namespace: DefaultNamespace}, " --out-dir=/srv/ch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.o.instanceFlags(); got != c.want {
				t.Errorf("instanceFlags() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestShellArg(t *testing.T) {
	for in, want := range map[string]string{
		"lab":                 "lab",
		"/home/me/.confighub": "/home/me/.confighub",
		"team a":              "'team a'",
		"it's":                `'it'\''s'`,
		"":                    "''",
	} {
		if got := shellArg(in); got != want {
			t.Errorf("shellArg(%q) = %q, want %q", in, got, want)
		}
	}
}
