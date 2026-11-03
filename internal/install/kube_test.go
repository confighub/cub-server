package install

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeManifests writes the files and returns their paths in name order, as
// applyManifests passes them.
func writeManifests(t *testing.T, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// names lists the metadata.name lines of a multi-document stream, in order.
func names(stream []byte) []string {
	var out []string
	for _, line := range strings.Split(string(stream), "\n") {
		if name, ok := strings.CutPrefix(line, "  name: "); ok {
			out = append(out, name)
		}
	}
	return out
}

// A resource in an earlier sync wave is applied before the others, even from a
// later file, and a file whose resources are in different waves is split
// between them.
func TestSyncWaves(t *testing.T) {
	paths := writeManifests(t, map[string]string{
		"00-namespace.yaml": `# The namespace.
apiVersion: v1
kind: Namespace
metadata:
  name: confighub
`,
		"30-database.yaml": `---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
---
apiVersion: v1
kind: Service
metadata:
  name: postgres-service
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
`,
		"40-deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: server
---
apiVersion: batch/v1
kind: Job
metadata:
  name: smoke-test
  annotations:
    argocd.argoproj.io/sync-wave: " 2 "
---
# Trailing comment only.
`,
		"50-service.yaml": `apiVersion: v1
kind: Service
metadata:
  name: server-service`,
	})

	waves, err := syncWaves(paths)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"postgres-service", "postgres"},
		{"confighub", "server-service", "server"},
		{"smoke-test"},
	}
	if len(waves) != len(want) {
		t.Fatalf("got %d waves, want %d:\n%s", len(waves), len(want), waves)
	}
	for i := range want {
		if got := names(waves[i]); strings.Join(got, ",") != strings.Join(want[i], ",") {
			t.Errorf("wave %d applies %v, want %v", i, got, want[i])
		}
	}
	// Each wave is a stream kubectl reads document by document.
	if got := strings.Count(string(waves[1]), "\n---\n"); got != 2 {
		t.Errorf("wave 1 has %d separators, want 2:\n%s", got, waves[1])
	}
	if !strings.HasPrefix(string(waves[1]), "# The namespace.\n") {
		t.Errorf("wave 1 lost the namespace's leading comment:\n%s", waves[1])
	}
}

// Within a wave, resources are applied by kind -- the Namespace, then the
// ServiceAccount, Secret and ConfigMap, the Services and the workloads -- whatever
// their files are called, and in file order where the kinds tie. A custom
// resource, whose kind declares no priority, goes last.
func TestSyncWavesOrderByKind(t *testing.T) {
	resource := func(apiVersion, kind, name string) string {
		return "apiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name + "\n"
	}
	paths := writeManifests(t, map[string]string{
		"configmap-env.yaml":         resource("v1", "ConfigMap", "env"),
		"deployment-server.yaml":     resource("apps/v1", "Deployment", "server"),
		"deployment-ui.yaml":         resource("apps/v1", "Deployment", "ui"),
		"ingressroute-api.yaml":      resource("traefik.io/v1alpha1", "IngressRoute", "api"),
		"namespace-confighub.yaml":   resource("v1", "Namespace", "confighub"),
		"secret-env.yaml":            resource("v1", "Secret", "secret"),
		"service-api.yaml":           resource("v1", "Service", "api-service"),
		"serviceaccount-server.yaml": resource("v1", "ServiceAccount", "account"),
	})

	waves, err := syncWaves(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 1 {
		t.Fatalf("got %d waves, want 1:\n%s", len(waves), waves)
	}
	want := "confighub,account,secret,env,api-service,server,ui,api"
	if got := strings.Join(names(waves[0]), ","); got != want {
		t.Errorf("applies %s, want %s", got, want)
	}
}

func TestSyncWavesRejectsANonInteger(t *testing.T) {
	paths := writeManifests(t, map[string]string{
		"40-deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: server
  annotations:
    argocd.argoproj.io/sync-wave: early
`,
	})
	_, err := syncWaves(paths)
	if err == nil || !strings.Contains(err.Error(), `"early" is not an integer`) {
		t.Fatalf("got %v, want an error naming the wave", err)
	}
}
