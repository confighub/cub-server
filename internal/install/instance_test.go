package install

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// outDirInCluster is an out-dir as a kind install leaves it, recording the
// cluster it created.
func outDirInCluster(t *testing.T, cluster string) string {
	t.Helper()
	dir := t.TempDir()
	def := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nname: " + cluster + "\nnodes:\n- role: control-plane\n"
	if err := os.WriteFile(filepath.Join(dir, "kind-cluster.yaml"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The reproduction in confighub/cub-server#3: an instance in cluster "a", then a
// command given only its out-dir. The default cluster name must not win.
func TestAnOutDirDecidesItsCluster(t *testing.T) {
	o := &Options{OutDir: outDirInCluster(t, "a")}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.ClusterName != "a" {
		t.Errorf("ClusterName = %q, want the recorded %q rather than the default", o.ClusterName, "a")
	}
}

func TestAClusterNameAgreeingWithTheOutDirIsAccepted(t *testing.T) {
	o := &Options{OutDir: outDirInCluster(t, "a"), ClusterName: "a"}
	if err := o.Defaults(); err != nil {
		t.Fatalf("a flag matching the record should be accepted: %v", err)
	}
}

func TestAClusterNameContradictingTheOutDirIsRefused(t *testing.T) {
	o := &Options{OutDir: outDirInCluster(t, "a"), ClusterName: "confighub"}
	err := o.Defaults()
	if err == nil || !strings.Contains(err.Error(), `kind cluster "a"`) {
		t.Fatalf("want a refusal naming the recorded cluster, got %v", err)
	}
}

func TestAKubeContextContradictingAKindOutDirIsRefused(t *testing.T) {
	o := &Options{OutDir: outDirInCluster(t, "a"), KubeContext: "prod"}
	if err := o.Defaults(); err == nil {
		t.Fatal("an out-dir recording a kind cluster and a kubeconfig context cannot both be right")
	}
}

// An install into a context records no kind cluster, and a fresh out-dir
// records nothing; neither may be given a cluster name it did not ask for
// beyond the ordinary default.
func TestAnOutDirWithoutARecordKeepsTheOrdinaryDefaults(t *testing.T) {
	o := &Options{OutDir: t.TempDir()}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.ClusterName != DefaultClusterName {
		t.Errorf("ClusterName = %q, want %q", o.ClusterName, DefaultClusterName)
	}
}

func TestDifferingKeysComparesOnlyWhatBothSidesHold(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	recorded := map[string]string{
		"DATABASE_URL":                    "postgres://a",
		"WORKER_MASTER_SECRET":            "mine",
		"KEYCLOAK_CLIENT_PRIVATE_KEY_JWK": "added by keycloak install, not applied yet",
	}

	same := map[string]string{
		"DATABASE_URL":         enc("postgres://a"),
		"WORKER_MASTER_SECRET": enc("mine"),
		"SOMETHING_ELSE":       enc("set by hand in the cluster"),
	}
	if got, err := differingKeys(recorded, same); err != nil || len(got) != 0 {
		t.Errorf("the same instance was reported as different: %v, %v", got, err)
	}

	stranger := map[string]string{
		"DATABASE_URL":         enc("postgres://b"),
		"WORKER_MASTER_SECRET": enc("theirs"),
	}
	got, err := differingKeys(recorded, stranger)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"DATABASE_URL", "WORKER_MASTER_SECRET"}; !reflect.DeepEqual(got, want) {
		t.Errorf("differingKeys = %v, want %v", got, want)
	}
}
