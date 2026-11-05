package install

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// The install namespace, on a cluster with admission policies.
//
// Many clusters run admission webhooks (OPA Gatekeeper, Kyverno) that refuse a
// Namespace without the labels the organization requires -- an owning team, a
// cost center -- and some have a platform team create every namespace through
// a process of its own. So an install can put labels and annotations on the
// Namespace it creates (--namespace-label, --namespace-annotation), or deploy
// into one that already exists and leave it alone (--create-namespace=false).
//
// Both work on the installer's render: the package renders its Namespace, and
// the labels and annotations are set on it with `cub function local`, the same
// functions ConfigHub runs on a Unit. A namespace that is not created is left
// out of the render altogether, so neither the apply nor the record holds it.
//
// What was chosen is kept in namespace.yaml in the out-dir, so a re-run,
// `keycloak install` and `uninstall` act on the namespace as the install did
// without being told again.

// namespaceSettingsFile records an instance's namespace settings, in its
// out-dir. Absent, the install created its namespace with only the package's
// labels.
const namespaceSettingsFile = "namespace.yaml"

// namespaceSettings is what namespaceSettingsFile holds.
type namespaceSettings struct {
	// Create is false when the namespace is the operator's, created before the
	// install and never touched by it. Nil is true.
	Create      *bool             `yaml:"create,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

func (s namespaceSettings) creates() bool { return s.Create == nil || *s.Create }

func (s namespaceSettings) isDefault() bool {
	return s.creates() && len(s.Labels) == 0 && len(s.Annotations) == 0
}

// readNamespaceSettings reads an instance's namespace settings; the defaults
// when it recorded none.
func readNamespaceSettings(outDir string) (namespaceSettings, error) {
	var s namespaceSettings
	path := filepath.Join(outDir, namespaceSettingsFile)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := yaml.Unmarshal(content, &s); err != nil {
		return s, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// writeNamespaceSettings records o's namespace settings in its out-dir, or
// removes the record when they are the defaults.
func writeNamespaceSettings(o *Options) error {
	path := filepath.Join(o.OutDir, namespaceSettingsFile)
	s := o.namespaceSettings()
	if s.isDefault() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	body, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	header := "# The install namespace's settings, chosen with --create-namespace,\n" +
		"# --namespace-label and --namespace-annotation, and kept by every later run.\n"
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return err
	}
	return writeFile(path, append([]byte(header), body...), 0o644)
}

// namespaceSettings projects o onto what is recorded.
func (o *Options) namespaceSettings() namespaceSettings {
	s := namespaceSettings{Labels: o.NamespaceLabels, Annotations: o.NamespaceAnnotations}
	if !o.createsNamespace() {
		create := false
		s.Create = &create
	}
	return s
}

// createsNamespace reports whether the install creates its namespace.
func (o *Options) createsNamespace() bool {
	return o.CreateNamespace == nil || *o.CreateNamespace
}

// customizesNamespace reports whether the namespace is anything but the one the
// package renders: labelled, annotated, or not created at all.
func (o *Options) customizesNamespace() bool {
	return !o.namespaceSettings().isDefault()
}

// adoptRecordedNamespace keeps the namespace settings of the previous run for
// each one no flag names. Like the StorageClass, they are chosen once, at
// install: a re-run, and 'keycloak install', render the Namespace again
// without being told, and one that lost its labels would be refused by the
// policy that asked for them.
func (o *Options) adoptRecordedNamespace() error {
	recorded, err := readNamespaceSettings(o.OutDir)
	if err != nil {
		return err
	}
	if o.CreateNamespace == nil {
		o.CreateNamespace = recorded.Create
	}
	// A namespace that is no longer created has no labels of the install's to
	// keep: they were set on a Namespace this run does not render.
	if !o.createsNamespace() {
		return nil
	}
	if !o.NamespaceLabelsSet {
		o.NamespaceLabels = recorded.Labels
	}
	if !o.NamespaceAnnotationsSet {
		o.NamespaceAnnotations = recorded.Annotations
	}
	return nil
}

// validateNamespaceSettings refuses settings for a Namespace that will not be
// rendered.
func (o *Options) validateNamespaceSettings() error {
	if o.createsNamespace() {
		return nil
	}
	if len(o.NamespaceLabels) > 0 || len(o.NamespaceAnnotations) > 0 {
		return fmt.Errorf("--create-namespace=false deploys into a namespace you created, which install leaves as it is; " +
			"give it its labels and annotations yourself, and drop --namespace-label and --namespace-annotation")
	}
	return nil
}

var (
	// qualifiedNamePart is the name half of a label or annotation key, and the
	// shape of a label value: Kubernetes' IsQualifiedName and IsValidLabelValue.
	qualifiedNamePart = regexp.MustCompile(`^([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]$`)
	// dnsSubdomain is a key's optional prefix.
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// validateMetadataKey checks a label or annotation key the way the API server
// does, so a bad one is refused before a cluster is created rather than at
// apply.
func validateMetadataKey(key string) error {
	name := key
	if prefix, rest, ok := strings.Cut(key, "/"); ok {
		if len(prefix) == 0 || len(prefix) > 253 || !dnsSubdomain.MatchString(prefix) {
			return fmt.Errorf("the prefix %q is not a DNS subdomain, like example.com", prefix)
		}
		name = rest
	}
	if len(name) == 0 || len(name) > 63 || !qualifiedNamePart.MatchString(name) {
		return fmt.Errorf("%q has to be 63 characters or fewer, of letters, digits, '-', '_' and '.', starting and ending with a letter or digit, after an optional prefix and '/'", name)
	}
	return nil
}

// validateLabelValue checks a label's value; an annotation's can be anything.
func validateLabelValue(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 63 || !qualifiedNamePart.MatchString(value) {
		return fmt.Errorf("a label value has to be 63 characters or fewer, of letters, digits, '-', '_' and '.', starting and ending with a letter or digit")
	}
	return nil
}

// ParseNamespaceLabels reads --namespace-label values, each key=value. A value
// of "" alone clears them, so a re-run can drop the labels an earlier one set.
func ParseNamespaceLabels(values []string) (map[string]string, error) {
	return parseKeyValues("--namespace-label", values, validateLabelValue)
}

// ParseNamespaceAnnotations reads --namespace-annotation values, each
// key=value, the way ParseNamespaceLabels reads labels.
func ParseNamespaceAnnotations(values []string) (map[string]string, error) {
	return parseKeyValues("--namespace-annotation", values, nil)
}

func parseKeyValues(flag string, values []string, validateValue func(string) error) (map[string]string, error) {
	out := map[string]string{}
	for _, kv := range values {
		if kv == "" {
			continue
		}
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("%s %q: expected key=value", flag, kv)
		}
		key = strings.TrimSpace(key)
		if err := validateMetadataKey(key); err != nil {
			return nil, fmt.Errorf("%s %q: %w", flag, kv, err)
		}
		if validateValue != nil {
			if err := validateValue(value); err != nil {
				return nil, fmt.Errorf("%s %q: %w", flag, kv, err)
			}
		}
		if prior, dup := out[key]; dup && prior != value {
			return nil, fmt.Errorf("%s names %s twice, as %q and %q", flag, key, prior, value)
		}
		out[key] = value
	}
	return out, nil
}

// cubFunctionLocal runs a ConfigHub function on one manifest with `cub function
// local`, and returns the manifest it produced. The arguments are passed by
// name, so a value that starts with '-' is not taken for a flag.
var cubFunctionLocal = func(ctx context.Context, manifest []byte, function string, args ...string) ([]byte, error) {
	cub, err := cubBinary()
	if err != nil {
		return nil, fmt.Errorf("setting the namespace's labels and annotations needs cub on your PATH")
	}
	argv := append([]string{"function", "local", "--data-only", "--toolchain", "Kubernetes/YAML", "--", "-", function}, args...)
	cmd := exec.CommandContext(ctx, cub, argv...)
	cmd.Env = cubEnv()
	cmd.Stdin = bytes.NewReader(manifest)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cub function local %s: %w\n%s", function, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// applyNamespaceSettings applies o's namespace settings to an installer render:
// it sets the labels and annotations on the rendered Namespace, or leaves the
// Namespace out when the install does not create it.
func applyNamespaceSettings(ctx context.Context, o *Options, files []config.File) ([]config.File, error) {
	if !o.customizesNamespace() {
		return files, nil
	}
	out := make([]config.File, 0, len(files))
	found := false
	for _, f := range files {
		doc := objectIn(f.Content, "Namespace", o.Namespace)
		if doc == nil {
			out = append(out, f)
			continue
		}
		found = true
		// The installer writes one object per file, so this file is the
		// Namespace, and what is done to it is done to the Namespace alone.
		if !onlyObject(f.Content) {
			return nil, fmt.Errorf("the installer rendered the Namespace %s in %s alongside other objects; expected it in a file of its own", o.Namespace, f.Path)
		}
		if !o.createsNamespace() {
			continue
		}
		content := f.Content
		var err error
		for _, key := range sortedKeys(o.NamespaceLabels) {
			if content, err = cubFunctionLocal(ctx, content, "set-label", "--label-key="+key, "--label-value="+o.NamespaceLabels[key]); err != nil {
				return nil, err
			}
		}
		for _, key := range sortedKeys(o.NamespaceAnnotations) {
			if content, err = cubFunctionLocal(ctx, content, "set-annotation", "--annotation-key="+key, "--annotation-value="+o.NamespaceAnnotations[key]); err != nil {
				return nil, err
			}
		}
		f.Content = content
		out = append(out, f)
	}
	if !found {
		return nil, fmt.Errorf("the installer rendered no Namespace %s to set labels and annotations on", o.Namespace)
	}
	return out, nil
}

// onlyObject reports whether content holds a single Kubernetes object.
func onlyObject(content []byte) bool {
	n := 0
	for _, text := range documentSeparator.Split(string(content), -1) {
		var meta struct {
			Kind string `yaml:"kind"`
		}
		if yaml.Unmarshal([]byte(text), &meta) == nil && meta.Kind != "" {
			n++
		}
	}
	return n == 1
}

// checkNamespaceExists refuses to deploy into a namespace the install does not
// create when it is not there. A kubeconfig that cannot read Namespaces, as
// one whose RBAC stops at the namespace, is warned about rather than refused:
// the apply says soon enough if the namespace is missing.
func checkNamespaceExists(ctx context.Context, u UI, o *Options, kube kubeEnv) error {
	if o.createsNamespace() {
		return nil
	}
	u.step("Checking the namespace %s", o.Namespace)
	out, err := run(ctx, toolKubectl.name, kube.args("get", "namespace", o.Namespace, "-o", "name", "--ignore-not-found")...)
	if err != nil {
		if strings.Contains(err.Error(), "Forbidden") || strings.Contains(err.Error(), "forbidden") {
			u.warn("could not read the namespace %s from here, so not checking that it exists", o.Namespace)
			return nil
		}
		return err
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf(
			"there is no namespace %q in %s.\n"+
				"    --create-namespace=false deploys into a namespace you created, and install does not create it.\n"+
				"    Create it with whatever labels and policies your cluster requires, then re-run",
			o.Namespace, kube.describe(o))
	}
	u.detail("it exists; install leaves it as it is")
	return nil
}

// uninstallFromNamespace deletes what an install applied to a namespace it did
// not create, and leaves the namespace: deleting it would take whatever else
// the operator keeps there. The operator's own Secret, with --existing-secret,
// is left too.
func uninstallFromNamespace(ctx context.Context, u UI, o *Options, kube kubeEnv) error {
	u.step("Deleting ConfigHub from namespace %q of %s, which install did not create", o.Namespace, kube.describe(o))
	var paths []string
	for _, dir := range []string{config.ConfigDir, config.SecretsDir} {
		found, err := filepath.Glob(filepath.Join(o.OutDir, dir, "*.yaml"))
		if err != nil {
			return err
		}
		for _, p := range found {
			if o.ExistingSecret != "" && p == filepath.Join(o.OutDir, config.SecretsDir, config.SecretFile) {
				continue
			}
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("%s holds no manifests to delete from namespace %q; delete what is there yourself", o.OutDir, o.Namespace)
	}
	sort.Strings(paths)
	args := []string{"delete", "-n", o.Namespace, "--ignore-not-found", "--wait=false"}
	for _, p := range paths {
		args = append(args, "-f", p)
	}
	if _, err := run(ctx, toolKubectl.name, kube.args(args...)...); err != nil {
		return err
	}
	// Deleting the StatefulSet leaves its claim, and the database's data.
	if o.Database == string(config.DatabaseInternal) {
		if _, err := run(ctx, toolKubectl.name, kube.args("delete", "pvc", databaseClaim, "-n", o.Namespace, "--ignore-not-found", "--wait=false")...); err != nil {
			return err
		}
	}
	u.detail("deleted %d manifests; the namespace %q was left in place", len(paths), o.Namespace)
	return nil
}
