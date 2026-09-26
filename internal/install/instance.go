package install

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// Which instance a command is acting on.
//
// An out-dir is one instance: it holds the generated secrets, the kubeconfig and
// the definition of the cluster those were applied to. Every command after the
// first locates the instance through it, so the out-dir, not a default, decides
// which cluster that is. A cluster name that merely matches is not evidence:
// every install that took the defaults is called `confighub`, and applying one
// instance's Secret over another's replaces its database password, its worker
// secret and its token-signing key.

// secretEnvName is the Secret the server reads its generated values from.
const secretEnvName = "confighub-secret-env"

// recordedClusterName is the kind cluster an install into outDir created, or ""
// when it created none (an install into a kubeconfig context, or no install).
func recordedClusterName(outDir string) (string, error) {
	path := filepath.Join(outDir, "kind-cluster.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	return doc.Name, nil
}

// adoptRecordedCluster takes the cluster from the out-dir when it names one.
//
// Runs before any default is applied, so that an unset --cluster-name reads as
// unset rather than as `confighub`. A flag that agrees with the record is
// accepted; one that disagrees is refused, because either the flag or the
// out-dir is wrong and nothing here can tell which.
func (o *Options) adoptRecordedCluster() error {
	if o.OutDir == "" {
		// The out-dir is then derived from the cluster name, so the two agree
		// by construction.
		return nil
	}
	recorded, err := recordedClusterName(o.OutDir)
	if err != nil || recorded == "" {
		return err
	}
	switch {
	case o.KubeContext != "":
		return fmt.Errorf(
			"%s is an instance in the kind cluster %q, not in the context %q.\n"+
				"    Drop --kube-context to act on it, or pass a different --out-dir",
			o.OutDir, recorded, o.KubeContext)
	case o.ClusterName == "":
		o.ClusterName = recorded
	case o.ClusterName != recorded:
		return fmt.Errorf(
			"%s is an instance in the kind cluster %q, not %q.\n"+
				"    Drop --cluster-name to act on it, or pass a different --out-dir",
			o.OutDir, recorded, o.ClusterName)
	}
	return nil
}

// existingCluster points kubectl at the cluster an existing instance is in,
// refusing to create one.
//
// For commands that add to an instance. Creating a cluster is right for an
// install and wrong here: an empty new cluster is not the instance, and
// deploying half of one into it only moves the failure later.
func existingCluster(u UI, o *Options) (kubeEnv, error) {
	if o.Target == TargetContext {
		u.step("Using existing cluster")
		u.detail("context: %s", o.KubeContext)
		return kubeEnv{context: o.KubeContext}, nil
	}

	u.step("Finding the cluster")
	exists, err := kindClusterExists(o.ClusterName)
	if err != nil {
		return kubeEnv{}, err
	}
	if !exists {
		return kubeEnv{}, fmt.Errorf(
			"the kind cluster %q that %s was installed into does not exist any more.\n"+
				"    The out-dir is a record of an instance that is gone. To start over:\n"+
				"      cub server uninstall%s\n"+
				"      cub server install%s",
			o.ClusterName, o.OutDir, outDirFlag(o), outDirFlag(o))
	}
	if err := requireUIPort(o); err != nil {
		return kubeEnv{}, err
	}
	kubeconfig, err := kubeconfigPath(o.OutDir)
	if err != nil {
		return kubeEnv{}, err
	}
	// Re-export: the cluster's API server port changes if its container was
	// recreated, and the file may be missing even though the cluster is not.
	if err := kindProvider().ExportKubeConfig(o.ClusterName, kubeconfig, false); err != nil {
		return kubeEnv{}, fmt.Errorf("exporting the kubeconfig for %q: %w", o.ClusterName, err)
	}
	u.detail("cluster %q, kubeconfig: %s", o.ClusterName, kubeconfig)
	return kubeEnv{kubeconfig: kubeconfig}, nil
}

// refuseIfAnotherInstance stops before applying this out-dir's Secret over one
// that belongs to a different instance.
//
// The cluster check above catches a wrong name; this catches everything else --
// a context pointing somewhere unexpected, a cluster recreated under the same
// name, an out-dir copied between machines. Generated values are what make an
// instance itself, so a live one that differs from the record is a stranger,
// whatever the cluster is called.
//
// requireLive is set by commands that add to an instance, where a missing
// Secret means there is no instance here to add to. An install is allowed to
// find nothing: that is a first install, or a resumed one that never got as far
// as applying.
func refuseIfAnotherInstance(ctx context.Context, kube kubeEnv, o *Options, requireLive bool) error {
	recordPath := filepath.Join(o.OutDir, config.SecretsDir, "confighub-secret.yaml")
	recorded, err := readStringData(recordPath)
	if err != nil {
		return err
	}
	if recorded == nil {
		return nil // nothing generated yet, so nothing to be overwritten with
	}

	out, err := run(ctx, toolKubectl.name, kube.args(
		"get", "secret", secretEnvName, "-n", o.Namespace,
		"-o", "json", "--ignore-not-found",
	)...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		if requireLive {
			return fmt.Errorf(
				"there is no ConfigHub instance in namespace %q of %s: its Secret %s is missing.\n"+
					"    %s describes one, but it is not in this cluster",
				o.Namespace, kube.describe(o), secretEnvName, o.OutDir)
		}
		return nil
	}

	var live struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &live); err != nil {
		return fmt.Errorf("parsing the Secret %s: %w", secretEnvName, err)
	}

	differ, err := differingKeys(recorded, live.Data)
	if err != nil {
		return err
	}
	if len(differ) == 0 {
		return nil
	}
	return fmt.Errorf(
		"namespace %q of %s holds a different ConfigHub instance than %s.\n"+
			"    Its %s differs from the one on record (%s), and applying would replace it.\n"+
			"    Pass the --out-dir of the instance that is there, or point at the cluster this one is in",
		o.Namespace, kube.describe(o), o.OutDir, secretEnvName, strings.Join(differ, ", "))
}

// differingKeys lists the keys set on both sides with different values.
//
// A key on only one side is not a difference. The record gains keys the
// cluster has not seen yet -- adding an identity provider adds its client key --
// and a partially applied run leaves the live side behind. What cannot happen to
// one instance is the same key holding two values.
func differingKeys(recorded map[string]string, liveData map[string]string) ([]string, error) {
	var differ []string
	for key, encoded := range liveData {
		want, ok := recorded[key]
		if !ok {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("the Secret %s has a key %s that is not valid base64: %w", secretEnvName, key, err)
		}
		if string(got) != want {
			differ = append(differ, key)
		}
	}
	sort.Strings(differ)
	return differ, nil
}

// readStringData reads a rendered Secret's values; nil, without error, when the
// file does not exist.
func readStringData(path string) (map[string]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.StringData == nil {
		doc.StringData = map[string]string{}
	}
	return doc.StringData, nil
}

// describe names the cluster for a message.
func (k kubeEnv) describe(o *Options) string {
	if k.context != "" {
		return fmt.Sprintf("context %q", k.context)
	}
	return fmt.Sprintf("kind cluster %q", o.ClusterName)
}
