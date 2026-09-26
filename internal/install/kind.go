package install

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
	"sigs.k8s.io/kind/pkg/cluster"
	kindlog "sigs.k8s.io/kind/pkg/log"
)

// The cluster is created in-process through kind's Go API rather than by
// shelling out to the kind binary.
//
// That is one fewer thing to install before ConfigHub can be evaluated, which
// matters more here than anywhere else: the whole point of this path is that
// someone with Docker can get an instance running without preparing anything.
// The cost is that kind's Go API carries no compatibility promise, so the
// version is pinned and upgrades are deliberate.

// kindConfig is the cluster definition.
//
// The extraPortMappings are the whole point. A NodePort is only reachable from
// the host if kind published the port when the node container was created, and
// that cannot be added afterwards -- which is why the NodePorts are fixed
// defaults rather than allocated once the cluster exists.
//
// That is also why the identity provider's port is published here, by an install
// that is not deploying one. `cub server keycloak install` comes later and
// cannot add a port to a running cluster, so the alternative to reserving it now
// is recreating the cluster then. A published port with nothing behind it costs
// a refused connection; not having one costs the cluster.
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
  - containerPort: %d
    hostPort: %d
    protocol: TCP
  - containerPort: %d
    hostPort: %d
    protocol: TCP
`

// kindProvider talks to Docker.
//
// Logging is discarded rather than forwarded: kind narrates its own progress in
// a form that assumes it owns the terminal, and this installer reports its steps
// itself. Failures come back as errors, which is what the caller acts on.
func kindProvider() *cluster.Provider {
	return cluster.NewProvider(
		cluster.ProviderWithDocker(),
		cluster.ProviderWithLogger(kindlog.NoopLogger{}),
	)
}

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
func kindClusterExists(name string) (bool, error) {
	names, err := kindProvider().List()
	if err != nil {
		return false, fmt.Errorf("listing kind clusters: %w", err)
	}
	for _, n := range names {
		if n == name {
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
//
// No context: kind's Create cannot be cancelled, and taking one would say it
// could.
func createKindCluster(u UI, o *Options) (string, error) {
	kubeconfig, err := kubeconfigPath(o.OutDir)
	if err != nil {
		return "", err
	}

	provider := kindProvider()

	exists, err := kindClusterExists(o.ClusterName)
	if err != nil {
		return "", err
	}
	if exists {
		if err := requireUIPort(o); err != nil {
			return "", err
		}
		u.detail("cluster %q already exists, reusing it", o.ClusterName)
		// Re-export: the file may be missing even though the cluster is not.
		if err := provider.ExportKubeConfig(o.ClusterName, kubeconfig, false); err != nil {
			return "", fmt.Errorf("exporting the kubeconfig for %q: %w", o.ClusterName, err)
		}
		return kubeconfig, nil
	}

	raw := fmt.Sprintf(kindConfigTemplate,
		o.ClusterName,
		o.APINodePort, o.APINodePort,
		o.OCINodePort, o.OCINodePort,
		o.KeycloakNodePort, o.KeycloakNodePort,
		o.UINodePort, o.UINodePort,
	)
	// Written out as well as passed in. It is the record of how the cluster was
	// created, next to everything else this install produced.
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(o.OutDir, "kind-cluster.yaml"), []byte(raw), 0o644); err != nil {
		return "", err
	}

	u.detail("creating kind cluster %q (this takes a minute)", o.ClusterName)
	u.detail("publishing host ports %d (API), %d (UI), %d (OCI), %d (reserved for Keycloak)",
		o.APINodePort, o.UINodePort, o.OCINodePort, o.KeycloakNodePort)

	if err := provider.Create(o.ClusterName,
		cluster.CreateWithRawConfig([]byte(raw)),
		cluster.CreateWithKubeconfigPath(kubeconfig),
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	); err != nil {
		return "", fmt.Errorf("creating the kind cluster %q: %w", o.ClusterName, err)
	}
	return kubeconfig, nil
}

// deleteKindCluster removes the cluster. The kubeconfig goes with the output
// directory, which the caller removes unless asked to keep it.
func deleteKindCluster(name, kubeconfig string) error {
	if err := kindProvider().Delete(name, kubeconfig); err != nil {
		return fmt.Errorf("deleting the kind cluster %q: %w", name, err)
	}
	return nil
}

// requireUIPort refuses a cluster that does not publish the UI's port.
//
// Clusters created before the UI was its own container publish three ports, and
// kind cannot add a fourth to a running cluster. Deploying anyway would leave a
// UI nothing outside the cluster can reach, so this says to recreate instead.
// Judged from the cluster definition the install recorded; a cluster with no
// record is let through, since there is nothing to judge from.
func requireUIPort(o *Options) error {
	content, err := os.ReadFile(filepath.Join(o.OutDir, "kind-cluster.yaml"))
	if err != nil {
		return nil
	}
	var cluster struct {
		Nodes []struct {
			ExtraPortMappings []struct {
				HostPort int `yaml:"hostPort"`
			} `yaml:"extraPortMappings"`
		} `yaml:"nodes"`
	}
	if yaml.Unmarshal(content, &cluster) != nil || len(cluster.Nodes) == 0 {
		return nil
	}
	for _, mapping := range cluster.Nodes[0].ExtraPortMappings {
		if mapping.HostPort == o.UINodePort {
			return nil
		}
	}
	return fmt.Errorf(
		"the kind cluster %q was created before the web UI ran as its own container, and does not publish a port for it.\n"+
			"    kind cannot add a port to a cluster that exists, so the instance has to be recreated:\n"+
			"      cub server uninstall%s\n"+
			"      cub server install%s\n"+
			"    Uninstalling deletes the cluster and its data",
		o.ClusterName, outDirFlag(o), outDirFlag(o))
}
