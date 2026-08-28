package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// kindConfig is the cluster definition.
//
// The extraPortMappings are the whole point. A NodePort is only reachable from
// the host if kind published the port when the node container was created, and
// that cannot be added afterwards -- which is why the NodePorts are fixed
// defaults rather than allocated once the cluster exists.
const kindConfigTemplate = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: %s
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: %d
    hostPort: %d
    protocol: TCP
  - containerPort: %d
    hostPort: %d
    protocol: TCP
`

// kubeconfigPath is a dedicated kubeconfig for a cluster this installer created.
//
// Kept out of ~/.kube/config on purpose: an evaluation install should not
// rewrite the file the user's real clusters live in, and uninstalling should not
// have to edit it back out.
//
// It sits in the install's own output directory rather than in a location
// derived from the cluster name, so that everything one install produced is in
// one place -- which is what makes --out-dir mean something and what lets the
// teardown be a directory removal.
func kubeconfigPath(outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(outDir, "kubeconfig"), nil
}

// kindClusterExists reports whether a kind cluster of this name is already there.
func kindClusterExists(ctx context.Context, name string) (bool, error) {
	out, err := run(ctx, toolKind.name, "get", "clusters")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

// createKindCluster creates the cluster if it is not already there, and returns
// the path to its kubeconfig.
//
// Re-running against an existing cluster is a no-op rather than an error: an
// install is expected to be re-run, and the cluster is the one part of it that
// is expensive to rebuild.
func createKindCluster(ctx context.Context, u UI, o *Options) (string, error) {
	kubeconfig, err := kubeconfigPath(o.OutDir)
	if err != nil {
		return "", err
	}

	exists, err := kindClusterExists(ctx, o.ClusterName)
	if err != nil {
		return "", err
	}
	if exists {
		u.detail("cluster %q already exists, reusing it", o.ClusterName)
		// Re-export: the file may be missing even though the cluster is not.
		if _, err := run(ctx, toolKind.name, "export", "kubeconfig",
			"--name", o.ClusterName, "--kubeconfig", kubeconfig); err != nil {
			return "", err
		}
		return kubeconfig, nil
	}

	cfg := fmt.Sprintf(kindConfigTemplate,
		o.ClusterName,
		o.APINodePort, o.APINodePort,
		o.OCINodePort, o.OCINodePort,
	)
	cfgPath := filepath.Join(o.OutDir, "kind-cluster.yaml")
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		return "", err
	}

	u.detail("creating kind cluster %q (this takes a minute)", o.ClusterName)
	u.detail("publishing host ports %d (API) and %d (OCI)", o.APINodePort, o.OCINodePort)
	if _, err := run(ctx, toolKind.name, "create", "cluster",
		"--config", cfgPath, "--kubeconfig", kubeconfig); err != nil {
		return "", err
	}
	return kubeconfig, nil
}

// deleteKindCluster removes the cluster. The kubeconfig goes with the output
// directory, which the caller removes unless asked to keep it.
func deleteKindCluster(ctx context.Context, name string) error {
	_, err := run(ctx, toolKind.name, "delete", "cluster", "--name", name)
	return err
}
