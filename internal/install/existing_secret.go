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

// A Secret the operator provides.
//
// Many clusters do not let an installer write long-lived credentials into a
// Kubernetes Secret: they are kept in a secret store and synced into the
// cluster by External Secrets, or by the Secrets Store CSI driver's
// secretObjects. With --existing-secret, the server and the bundled database
// read the Secret named, and install never applies one of its own.
//
// Install still generates the values -- the token signing key, the database
// password -- and keeps them in the out-dir as before, so a re-run reuses them
// rather than minting new ones. There they are for the operator to load into
// their store, once. Before it deploys anything, install checks that the Secret
// is in the cluster with every key the server needs, so a missing one is a
// message rather than a pod that cannot start.

// recordedExistingSecret is the Secret the instance in outDir was told to read
// with --existing-secret, or "" when it reads the one install renders.
func recordedExistingSecret(outDir string) (string, error) {
	path := filepath.Join(outDir, config.SecretsDir, config.SecretFile)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var doc struct {
		Metadata struct {
			Name        string            `yaml:"name"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc.Metadata.Annotations[config.ExistingSecretAnnotation] != "true" {
		return "", nil
	}
	return doc.Metadata.Name, nil
}

// adoptRecordedSecret keeps --existing-secret from the previous render when the
// flag is not given. Chosen once, at install, like the StorageClass: a re-run,
// and 'keycloak install', render the workloads again without being told, and
// one that went back to the rendered Secret would apply credentials the
// operator chose to keep out of the cluster. --existing-secret="" is how to go
// back on purpose.
func (o *Options) adoptRecordedSecret() error {
	if o.ExistingSecretSet {
		return nil
	}
	name, err := recordedExistingSecret(o.OutDir)
	if err != nil {
		return err
	}
	o.ExistingSecret = name
	return nil
}

// secretName is the Secret the instance's workloads read.
func (o *Options) secretName() string {
	if o.ExistingSecret != "" {
		return o.ExistingSecret
	}
	return config.DefaultSecretName
}

// appliedFiles leaves out what an install must not apply: with
// --existing-secret, the server's Secret, which the operator provides, and the
// file of its values.
func appliedFiles(o *Options, files []config.File) []config.File {
	if o.ExistingSecret == "" {
		return files
	}
	var out []config.File
	for _, f := range files {
		switch f.Path {
		case config.SecretsDir + "/" + config.SecretFile, config.SecretsDir + "/" + config.SecretValuesFile:
			continue
		}
		out = append(out, f)
	}
	return out
}

// requiredSecretKeys are the keys the operator's Secret has to hold: every one
// install would have rendered into its own, and DATABASE_URL, which an install
// against an external database may have left for the operator to supply.
func requiredSecretKeys(recorded map[string]string) []string {
	keys := make([]string, 0, len(recorded)+1)
	for k := range recorded {
		keys = append(keys, k)
	}
	if _, ok := recorded["DATABASE_URL"]; !ok {
		keys = append(keys, "DATABASE_URL")
	}
	sort.Strings(keys)
	return keys
}

// pinnedSecretKeys are values the rendered manifests depend on, so the
// operator's Secret has to hold the very value install generated rather than
// one of its own: Keycloak imports the public half of the server's client key
// with its realm, and a different private half signs assertions it rejects.
var pinnedSecretKeys = []string{config.KeycloakClientKeyEnv}

// secretProblems compares the operator's Secret with what the server needs:
// the required keys it lacks, or holds empty, and the pinned ones whose value
// is not the one install generated.
func secretProblems(recorded map[string]string, liveData map[string]string) (missing, differ []string, err error) {
	for _, key := range requiredSecretKeys(recorded) {
		if liveData[key] == "" {
			missing = append(missing, key)
		}
	}
	for _, key := range pinnedSecretKeys {
		want, ok := recorded[key]
		encoded := liveData[key]
		if !ok || encoded == "" {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, nil, fmt.Errorf("the Secret has a key %s that is not valid base64: %w", key, err)
		}
		if string(got) != want {
			differ = append(differ, key)
		}
	}
	return missing, differ, nil
}

// checkExistingSecret refuses to deploy against an operator's Secret that is
// not there, or that lacks a key the server needs. The out-dir has to have
// been rendered already: its Secret is the list of keys.
func checkExistingSecret(ctx context.Context, u UI, o *Options, kube kubeEnv) error {
	if o.ExistingSecret == "" {
		return nil
	}
	u.step("Checking the Secret %s", o.ExistingSecret)

	recorded, err := readStringData(filepath.Join(o.OutDir, config.SecretsDir, config.SecretFile))
	if err != nil {
		return err
	}
	values := filepath.Join(o.OutDir, config.SecretsDir, config.SecretValuesFile)

	out, err := run(ctx, toolKubectl.name, kube.args(
		"get", "secret", o.ExistingSecret, "-n", o.Namespace,
		"-o", "json", "--ignore-not-found",
	)...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf(
			"there is no Secret %s in namespace %q of %s.\n"+
				"    --existing-secret names a Secret you provide, and install does not create it.\n"+
				"    Load the values in %s into your secret store,\n"+
				"    and have External Secrets or the Secrets Store CSI driver sync them to that Secret\n"+
				"    (creating the namespace first if it is not there yet). Then re-run.\n\n"+
				"    It needs the keys: %s",
			o.ExistingSecret, o.Namespace, kube.describe(o), values,
			strings.Join(requiredSecretKeys(recorded), ", "))
	}

	var live struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &live); err != nil {
		return fmt.Errorf("parsing the Secret %s: %w", o.ExistingSecret, err)
	}
	missing, differ, err := secretProblems(recorded, live.Data)
	if err != nil {
		return fmt.Errorf("%s: %w", o.ExistingSecret, err)
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"the Secret %s in namespace %q is missing %s.\n"+
				"    The values install generated are in %s;\n"+
				"    add them to your secret store, wait for the Secret to sync, and re-run",
			o.ExistingSecret, o.Namespace, strings.Join(missing, ", "), values)
	}
	if len(differ) > 0 {
		return fmt.Errorf(
			"the Secret %s in namespace %q holds a different %s than install generated.\n"+
				"    The rendered manifests depend on that value, so it has to be the one in %s",
			o.ExistingSecret, o.Namespace, strings.Join(differ, ", "), values)
	}
	u.detail("it has every key the server reads (%d)", len(requiredSecretKeys(recorded)))
	return nil
}
