package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

func TestParseNamespaceLabels(t *testing.T) {
	got, err := ParseNamespaceLabels([]string{"app.example.com/owner=platform", "cost-center=1234", "empty="})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"app.example.com/owner": "platform", "cost-center": "1234", "empty": ""}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// "" alone clears them, so a re-run can drop what an earlier one set.
	if got, err := ParseNamespaceLabels([]string{""}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("\"\" gave %v, %v; want an empty set", got, err)
	}

	for _, bad := range []string{
		"no-equals",
		"=value",
		"Bad_Prefix.com/name=x",
		"name=has space",
		"name=" + strings.Repeat("a", 64),
		"-leading=x",
	} {
		if _, err := ParseNamespaceLabels([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseNamespaceLabels([]string{"a=1", "a=2"}); err == nil {
		t.Error("accepted one key with two values")
	}
}

// An annotation's value can be anything; only its key is checked.
func TestParseNamespaceAnnotations(t *testing.T) {
	got, err := ParseNamespaceAnnotations([]string{"example.com/contact=Platform Team <platform@example.com>, on call"})
	if err != nil {
		t.Fatal(err)
	}
	if got["example.com/contact"] != "Platform Team <platform@example.com>, on call" {
		t.Errorf("got %v", got)
	}
	if _, err := ParseNamespaceAnnotations([]string{"bad key=x"}); err == nil {
		t.Error("accepted a key with a space")
	}
}

// Namespace settings are applied to the installer's render, so they imply it,
// and refuse --installer=false.
func TestNamespaceSettingsImplyTheInstaller(t *testing.T) {
	o := Options{OutDir: t.TempDir(), NamespaceLabels: map[string]string{"owner": "platform"}, NamespaceLabelsSet: true}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if !o.Installer {
		t.Error("--namespace-label did not imply --installer")
	}

	create := false
	o = Options{OutDir: t.TempDir(), CreateNamespace: &create}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if !o.Installer {
		t.Error("--create-namespace=false did not imply --installer")
	}

	o = Options{OutDir: t.TempDir(), CreateNamespace: &create, InstallerSet: true}
	if err := o.Defaults(); err == nil || !strings.Contains(err.Error(), "--installer=false") {
		t.Errorf("got %v, want a refusal naming --installer=false", err)
	}

	// The defaults leave the renderer alone.
	o = Options{OutDir: t.TempDir()}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.Installer {
		t.Error("an install with the default namespace took the installer")
	}
}

// A namespace install does not create has no Namespace to label.
func TestCreateNamespaceFalseRefusesLabels(t *testing.T) {
	create := false
	o := Options{
		OutDir: t.TempDir(), CreateNamespace: &create,
		NamespaceAnnotations: map[string]string{"a": "b"}, NamespaceAnnotationsSet: true,
	}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if err := o.Validate(); err == nil || !strings.Contains(err.Error(), "--create-namespace=false") {
		t.Errorf("got %v, want a refusal naming --create-namespace=false", err)
	}
}

// What an install chose is recorded, and a later run with no flags keeps it;
// a flag replaces it; the defaults leave no record.
func TestNamespaceSettingsAreKept(t *testing.T) {
	dir := t.TempDir()
	first := Options{
		OutDir:          dir,
		NamespaceLabels: map[string]string{"owner": "platform", "cost-center": "1234"}, NamespaceLabelsSet: true,
		NamespaceAnnotations: map[string]string{"example.com/contact": "platform@example.com"}, NamespaceAnnotationsSet: true,
	}
	if err := first.Defaults(); err != nil {
		t.Fatal(err)
	}
	if err := writeNamespaceSettings(&first); err != nil {
		t.Fatal(err)
	}

	again := Options{OutDir: dir}
	if err := again.Defaults(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(again.NamespaceLabels) != fmt.Sprint(first.NamespaceLabels) ||
		fmt.Sprint(again.NamespaceAnnotations) != fmt.Sprint(first.NamespaceAnnotations) {
		t.Errorf("a re-run read back %v and %v", again.NamespaceLabels, again.NamespaceAnnotations)
	}
	if !again.Installer || !again.createsNamespace() {
		t.Errorf("a re-run: installer %v, creates the namespace %v", again.Installer, again.createsNamespace())
	}

	// --namespace-label replaces the labels and keeps the annotations.
	changed := Options{OutDir: dir, NamespaceLabels: map[string]string{"owner": "data"}, NamespaceLabelsSet: true}
	if err := changed.Defaults(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(changed.NamespaceLabels) != "map[owner:data]" || len(changed.NamespaceAnnotations) != 1 {
		t.Errorf("got labels %v and annotations %v", changed.NamespaceLabels, changed.NamespaceAnnotations)
	}

	// Clearing both goes back to the defaults, and removes the record.
	cleared := Options{OutDir: dir, NamespaceLabels: map[string]string{}, NamespaceLabelsSet: true,
		NamespaceAnnotations: map[string]string{}, NamespaceAnnotationsSet: true}
	if err := cleared.Defaults(); err != nil {
		t.Fatal(err)
	}
	if err := writeNamespaceSettings(&cleared); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, namespaceSettingsFile)); !os.IsNotExist(err) {
		t.Errorf("the defaults left a record: %v", err)
	}

	// --create-namespace=false is kept too, and drops the labels recorded for
	// a Namespace it no longer renders.
	if err := writeNamespaceSettings(&first); err != nil {
		t.Fatal(err)
	}
	create := false
	external := Options{OutDir: dir, CreateNamespace: &create}
	if err := external.Defaults(); err != nil {
		t.Fatal(err)
	}
	if err := external.Validate(); err != nil && strings.Contains(err.Error(), "--create-namespace") {
		t.Errorf("--create-namespace=false over recorded labels: %v", err)
	}
	if len(external.NamespaceLabels) != 0 || len(external.NamespaceAnnotations) != 0 {
		t.Errorf("kept %v and %v for a namespace it does not create", external.NamespaceLabels, external.NamespaceAnnotations)
	}
	if err := writeNamespaceSettings(&external); err != nil {
		t.Fatal(err)
	}
	later := Options{OutDir: dir}
	if err := later.Defaults(); err != nil {
		t.Fatal(err)
	}
	if later.createsNamespace() {
		t.Error("a re-run forgot --create-namespace=false")
	}
}

const renderedNamespace = `apiVersion: v1
kind: Namespace
metadata:
  name: confighub
  annotations:
    argocd.argoproj.io/sync-wave: "-2"
  labels:
    pod-security.kubernetes.io/enforce: restricted
`

func installerRender() []config.File {
	return []config.File{
		{Path: config.ConfigDir + "/namespace-confighub.yaml", Content: []byte(renderedNamespace)},
		{Path: config.ConfigDir + "/configmap-confighub-confighub-env.yaml", Content: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: confighub-env\n  namespace: confighub\n")},
	}
}

// Labels and annotations are set on the rendered Namespace, one function at a
// time, and nothing else is touched.
func TestApplyNamespaceSettings(t *testing.T) {
	var calls []string
	stub := func(ctx context.Context, manifest []byte, function string, args ...string) ([]byte, error) {
		calls = append(calls, function+" "+strings.Join(args, " "))
		return append(manifest, []byte("# "+function+"\n")...), nil
	}
	saved := cubFunctionLocal
	cubFunctionLocal = stub
	t.Cleanup(func() { cubFunctionLocal = saved })

	o := &Options{
		Namespace:            "confighub",
		NamespaceLabels:      map[string]string{"b": "2", "a": "1"},
		NamespaceAnnotations: map[string]string{"note": "-x"},
	}
	files, err := applyNamespaceSettings(context.Background(), o, installerRender())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"set-label --label-key=a --label-value=1",
		"set-label --label-key=b --label-value=2",
		"set-annotation --annotation-key=note --annotation-value=-x",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if len(files) != 2 || !strings.Contains(string(files[0].Content), "# set-annotation") ||
		string(files[1].Content) != string(installerRender()[1].Content) {
		t.Errorf("unexpected render: %+v", files)
	}

	// A namespace install does not create is left out of the render.
	create := false
	calls = nil
	files, err = applyNamespaceSettings(context.Background(), &Options{Namespace: "confighub", CreateNamespace: &create}, installerRender())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || objectIn(files[0].Content, "Namespace", "confighub") != nil || len(calls) != 0 {
		t.Errorf("rendered %d files, ran %v", len(files), calls)
	}

	// The defaults change nothing and run nothing.
	files, err = applyNamespaceSettings(context.Background(), &Options{Namespace: "confighub"}, installerRender())
	if err != nil || len(files) != 2 || len(calls) != 0 {
		t.Errorf("the defaults: %d files, ran %v, %v", len(files), calls, err)
	}

	// A render without the Namespace is refused rather than left unlabelled.
	if _, err := applyNamespaceSettings(context.Background(), o, installerRender()[1:]); err == nil {
		t.Error("labelled a render that has no Namespace")
	}
}

// The real cub sets them, keeping the package's own labels. Skipped without
// cub on PATH.
func TestApplyNamespaceSettingsWithCub(t *testing.T) {
	if _, err := exec.LookPath("cub"); err != nil {
		t.Skip("no cub on PATH")
	}
	o := &Options{
		Namespace:            "confighub",
		NamespaceLabels:      map[string]string{"app.example.com/owner": "platform"},
		NamespaceAnnotations: map[string]string{"example.com/contact": "-platform, on call"},
	}
	files, err := applyNamespaceSettings(context.Background(), o, installerRender())
	if err != nil {
		t.Fatal(err)
	}
	var ns struct {
		Metadata struct {
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(files[0].Content, &ns); err != nil {
		t.Fatalf("%v\n%s", err, files[0].Content)
	}
	for k, v := range map[string]string{"app.example.com/owner": "platform", "pod-security.kubernetes.io/enforce": "restricted"} {
		if ns.Metadata.Labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, ns.Metadata.Labels[k], v)
		}
	}
	for k, v := range map[string]string{"example.com/contact": "-platform, on call", "argocd.argoproj.io/sync-wave": "-2"} {
		if ns.Metadata.Annotations[k] != v {
			t.Errorf("annotation %s = %q, want %q", k, ns.Metadata.Annotations[k], v)
		}
	}
}

// On a cluster of the operator's own, the interview asks whether the namespace
// exists and, if install creates it, for its labels and annotations.
func TestInterviewAsksAboutTheNamespace(t *testing.T) {
	c := &fakeCluster{classes: []storageClass{{Name: "standard", Default: true}}}
	base := func() Options {
		return Options{
			Target: TargetContext, KubeContext: "prod", APINodePort: 32180, OCIURL: "https://oci.example.com",
			Database: string(config.DatabaseInternal), NewAdminKey: true, Image: "img:v1",
			OutDir: t.TempDir(),
		}
	}

	o := base()
	var out strings.Builder
	in := "1\nowner=platform\nbad key=x\ncost-center=1234\n\nexample.com/contact=platform@example.com\n\n"
	if err := interview(strings.NewReader(in), &out, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(o.NamespaceLabels) != "map[cost-center:1234 owner:platform]" || !o.NamespaceLabelsSet {
		t.Errorf("labels %v (set %v)", o.NamespaceLabels, o.NamespaceLabelsSet)
	}
	if o.NamespaceAnnotations["example.com/contact"] != "platform@example.com" {
		t.Errorf("annotations %v", o.NamespaceAnnotations)
	}
	if !strings.Contains(out.String(), "bad key") {
		t.Errorf("the bad label was not refused:\n%s", out.String())
	}

	// Blank answers leave everything unset, so a re-run keeps what was recorded.
	o = base()
	if err := interview(strings.NewReader("\n\n\n"), &strings.Builder{}, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if o.CreateNamespace != nil || o.NamespaceLabelsSet || o.NamespaceAnnotationsSet {
		t.Errorf("blank answers set %v, %v, %v", o.CreateNamespace, o.NamespaceLabelsSet, o.NamespaceAnnotationsSet)
	}

	// A namespace that exists already has nothing else to ask.
	o = base()
	if err := interview(strings.NewReader("2\n"), &strings.Builder{}, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if o.createsNamespace() {
		t.Error("answering that the namespace exists still creates it")
	}
}

// fakeKubectl replaces runKubectl for a test, recording each invocation and
// answering with respond.
func fakeKubectl(t *testing.T, respond func(args []string) (string, error)) *[][]string {
	t.Helper()
	var calls [][]string
	saved := runKubectl
	runKubectl = func(ctx context.Context, args ...string) (string, error) {
		calls = append(calls, args)
		return respond(args)
	}
	t.Cleanup(func() { runKubectl = saved })
	return &calls
}

// A label or annotation that would replace one the package sets is refused,
// and one that repeats the package's value is not.
func TestApplyNamespaceSettingsRefusesReplacingPackageMetadata(t *testing.T) {
	saved := cubFunctionLocal
	cubFunctionLocal = func(ctx context.Context, manifest []byte, function string, args ...string) ([]byte, error) {
		return manifest, nil
	}
	t.Cleanup(func() { cubFunctionLocal = saved })

	for name, o := range map[string]*Options{
		"label": {Namespace: "confighub", NamespaceLabels: map[string]string{
			"pod-security.kubernetes.io/enforce": "privileged", "owner": "platform"}},
		"annotation": {Namespace: "confighub", NamespaceAnnotations: map[string]string{
			"argocd.argoproj.io/sync-wave": "5"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := applyNamespaceSettings(context.Background(), o, installerRender())
			if err == nil || !strings.Contains(err.Error(), "--create-namespace=false") {
				t.Errorf("got %v, want a refusal pointing at --create-namespace=false", err)
			}
			if err != nil && strings.Contains(err.Error(), "owner") {
				t.Errorf("named a label the package does not set: %v", err)
			}
		})
	}

	same := &Options{Namespace: "confighub", NamespaceLabels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}}
	if _, err := applyNamespaceSettings(context.Background(), same, installerRender()); err != nil {
		t.Errorf("refused the package's own value: %v", err)
	}
}

func TestCheckNamespaceExists(t *testing.T) {
	no := false
	o := &Options{Namespace: "confighub", CreateNamespace: &no, Target: TargetContext, KubeContext: "prod"}
	kube := kubeEnv{context: "prod"}

	calls := fakeKubectl(t, func(args []string) (string, error) { return "namespace/confighub\n", nil })
	if err := checkNamespaceExists(context.Background(), UI{Out: &strings.Builder{}}, o, kube); err != nil {
		t.Errorf("refused a namespace that exists: %v", err)
	}
	if got := strings.Join((*calls)[0], " "); !strings.Contains(got, "get namespace confighub") || !strings.Contains(got, "prod") {
		t.Errorf("ran kubectl %s", got)
	}

	fakeKubectl(t, func(args []string) (string, error) { return "", nil })
	if err := checkNamespaceExists(context.Background(), UI{Out: &strings.Builder{}}, o, kube); err == nil || !strings.Contains(err.Error(), "no namespace") {
		t.Errorf("got %v, want a refusal for a missing namespace", err)
	}

	var out strings.Builder
	fakeKubectl(t, func(args []string) (string, error) {
		return "", errors.New(`Error from server (Forbidden): namespaces "confighub" is forbidden: User "dev" cannot get resource "namespaces"`)
	})
	if err := checkNamespaceExists(context.Background(), UI{Out: &out}, o, kube); err != nil {
		t.Errorf("an RBAC refusal stopped the install: %v", err)
	}
	if !strings.Contains(out.String(), "could not read") {
		t.Errorf("an RBAC refusal was not warned about:\n%s", out.String())
	}

	fakeKubectl(t, func(args []string) (string, error) { return "", errors.New("connection refused") })
	if err := checkNamespaceExists(context.Background(), UI{Out: &strings.Builder{}}, o, kube); err == nil {
		t.Error("an unreachable cluster passed the check")
	}

	// A namespace install creates is not checked.
	calls = fakeKubectl(t, func(args []string) (string, error) { return "", nil })
	if err := checkNamespaceExists(context.Background(), UI{Out: &strings.Builder{}}, &Options{Namespace: "confighub"}, kube); err != nil || len(*calls) != 0 {
		t.Errorf("checked a namespace install creates: %v, %v", *calls, err)
	}
}

// writeUninstallRecord writes an out-dir as an install into a namespace it did
// not create leaves it.
func writeUninstallRecord(t *testing.T, dir string) {
	t.Helper()
	for path, content := range map[string]string{
		config.ConfigDir + "/deployment-confighub-confighub.yaml":           "kind: Deployment\n",
		config.ConfigDir + "/statefulset-confighub-confighub-postgres.yaml": "kind: StatefulSet\n",
		config.SecretsDir + "/" + config.SecretFile:                         "kind: Secret\n",
		config.SecretsDir + "/" + config.KeycloakSecretFile:                 "kind: Secret\n",
		config.SecretsDir + "/" + config.SecretValuesFile:                   "{}\n",
		namespaceSettingsFile: "create: false\n",
	} {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Uninstalling from a namespace install did not create deletes the manifests
// it applied and the database's claim, and never the namespace.
func TestUninstallFromNamespace(t *testing.T) {
	dir := t.TempDir()
	writeUninstallRecord(t, dir)
	calls := fakeKubectl(t, func(args []string) (string, error) { return "", nil })
	o := &Options{Namespace: "confighub", OutDir: dir, Database: string(config.DatabaseInternal)}
	if err := uninstallFromNamespace(context.Background(), UI{Out: &strings.Builder{}}, o, kubeEnv{context: "prod"}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("ran kubectl %d times: %v", len(*calls), *calls)
	}
	deleted := flagValues((*calls)[0], "-f")
	for _, want := range []string{
		filepath.Join(dir, config.ConfigDir, "deployment-confighub-confighub.yaml"),
		filepath.Join(dir, config.ConfigDir, "statefulset-confighub-confighub-postgres.yaml"),
		filepath.Join(dir, config.SecretsDir, config.SecretFile),
		filepath.Join(dir, config.SecretsDir, config.KeycloakSecretFile),
	} {
		if !slices.Contains(deleted, want) {
			t.Errorf("did not delete %s: %v", want, deleted)
		}
	}
	for _, call := range *calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "namespace confighub") || strings.Contains(joined, ".json") {
			t.Errorf("ran kubectl %s", joined)
		}
	}
	if got := strings.Join((*calls)[1], " "); !strings.Contains(got, "delete pvc "+databaseClaim) {
		t.Errorf("did not delete the database's claim: %s", got)
	}

	// The operator's Secret is theirs, and is left.
	calls = fakeKubectl(t, func(args []string) (string, error) { return "", nil })
	o.ExistingSecret = "confighub-from-vault"
	if err := uninstallFromNamespace(context.Background(), UI{Out: &strings.Builder{}}, o, kubeEnv{context: "prod"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(flagValues((*calls)[0], "-f"), filepath.Join(dir, config.SecretsDir, config.SecretFile)) {
		t.Error("deleted the Secret the operator provides")
	}

	// Nothing on record is refused rather than deleting nothing and reporting success.
	if err := uninstallFromNamespace(context.Background(), UI{Out: &strings.Builder{}}, &Options{Namespace: "confighub", OutDir: t.TempDir()}, kubeEnv{}); err == nil {
		t.Error("uninstalled with no manifests on record")
	}
}

// Uninstall itself takes the namespace-preserving path from the record.
// Skipped without kubectl, which Uninstall requires before it starts.
func TestUninstallLeavesTheOperatorsNamespace(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("no kubectl on PATH")
	}
	dir := t.TempDir()
	writeUninstallRecord(t, dir)
	calls := fakeKubectl(t, func(args []string) (string, error) { return "", nil })
	o := &Options{KubeContext: "prod", OutDir: dir}
	if err := Uninstall(context.Background(), UI{Out: &strings.Builder{}}, o, true); err != nil {
		t.Fatal(err)
	}
	if len(*calls) == 0 {
		t.Fatal("ran no kubectl")
	}
	for _, call := range *calls {
		if slices.Contains(call, "namespace") {
			t.Errorf("deleted the namespace: %v", call)
		}
	}
}

// A kind cluster this would create has no namespace of the operator's; one on
// a kubeconfig context is checked against the cluster instead.
func TestRefuseNamespaceInNewCluster(t *testing.T) {
	no := false
	if err := refuseNamespaceInNewCluster(&Options{Target: TargetContext, CreateNamespace: &no}); err != nil {
		t.Errorf("refused a kubeconfig context: %v", err)
	}
	if err := refuseNamespaceInNewCluster(&Options{Target: TargetKind}); err != nil {
		t.Errorf("refused a namespace install creates: %v", err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker, which kind needs to list its clusters")
	}
	err := refuseNamespaceInNewCluster(&Options{Target: TargetKind, ClusterName: "cub-server-test-no-such-cluster", CreateNamespace: &no})
	if err == nil || !strings.Contains(err.Error(), "does not exist yet") {
		t.Errorf("got %v, want a refusal for a cluster that does not exist", err)
	}
}

// An instance rendered with this plugin's manifests is said to be switching.
func TestRendererSwitch(t *testing.T) {
	dir := t.TempDir()
	if got := rendererSwitch(dir); got != "" {
		t.Errorf("a first install is switching: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, config.ConfigDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.ConfigDir, "40-deployment.yaml"), []byte("kind: Deployment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rendererSwitch(dir); !strings.Contains(got, "switching") {
		t.Errorf("a plugin render is not switching: %q", got)
	}
	if err := os.MkdirAll(installerDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := rendererSwitch(dir); got != "" {
		t.Errorf("an installer render is switching: %q", got)
	}
}

// A function's arguments, which carry values, stay out of the error.
func TestRunCubInLeavesFunctionArgumentsOutOfErrors(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "cub")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'no such function' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runCubIn(context.Background(), []byte("kind: Namespace\n"), fake,
		"function", "local", "--data-only", "--", "-", "set-annotation", "--annotation-value=do-not-print")
	if err == nil {
		t.Fatal("a failing cub succeeded")
	}
	if strings.Contains(err.Error(), "do-not-print") {
		t.Errorf("the error has the function's arguments: %v", err)
	}
	if !strings.Contains(err.Error(), "function local") || !strings.Contains(err.Error(), "no such function") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

// The interview offers the instance's recorded choice as the default, and
// answering with it leaves the choice to Defaults.
func TestInterviewOffersTheRecordedNamespace(t *testing.T) {
	c := &fakeCluster{classes: []storageClass{{Name: "standard", Default: true}}}
	base := func(dir string) Options {
		return Options{
			Target: TargetContext, KubeContext: "prod", APINodePort: 32180, OCIURL: "https://oci.example.com",
			Database: string(config.DatabaseInternal), NewAdminKey: true, Image: "img:v1", OutDir: dir,
		}
	}

	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, namespaceSettingsFile), []byte("create: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := base(external)
	var out strings.Builder
	if err := interview(strings.NewReader("\n"), &out, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if o.CreateNamespace != nil || o.NamespaceLabelsSet {
		t.Errorf("taking the recorded default set %v, labels set %v", o.CreateNamespace, o.NamespaceLabelsSet)
	}
	if !strings.Contains(out.String(), "as the previous install chose") || strings.Contains(out.String(), "Namespace label") {
		t.Errorf("did not offer the recorded choice, or asked for labels anyway:\n%s", out.String())
	}

	// Choosing to create it now sets that, and asks for its labels.
	o = base(external)
	if err := interview(strings.NewReader("1\nowner=platform\n\n\n"), &strings.Builder{}, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if !o.createsNamespace() || o.CreateNamespace == nil || o.NamespaceLabels["owner"] != "platform" {
		t.Errorf("create %v, labels %v", o.CreateNamespace, o.NamespaceLabels)
	}

	// Recorded labels are listed, and a blank answer keeps them.
	labelled := t.TempDir()
	if err := os.WriteFile(filepath.Join(labelled, namespaceSettingsFile), []byte("labels:\n  owner: platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o = base(labelled)
	out.Reset()
	if err := interview(strings.NewReader("\n\n\n"), &out, &o, c.connect); err != nil {
		t.Fatal(err)
	}
	if o.NamespaceLabelsSet || !strings.Contains(out.String(), "owner=platform") {
		t.Errorf("labels set %v; the recorded ones were not listed:\n%s", o.NamespaceLabelsSet, out.String())
	}
}
