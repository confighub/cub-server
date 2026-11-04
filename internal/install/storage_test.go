package install

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// writeRender renders an instance with these options into dir, as an install
// would leave it.
func writeStorageRender(t *testing.T, dir string, opts config.Options) {
	t.Helper()
	opts.Image = "ghcr.io/confighubai/confighub:test"
	opts.UIImage = "ghcr.io/confighub/ui:test"
	opts.Defaults()
	surface, err := config.Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := config.Render(surface, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// The StorageClass and size an install was given are read back from its
// render, so a re-run, or 'keycloak install', that renders the StatefulSet
// again without the flags keeps them; the API server would refuse a claim
// template that changed.
func TestRerunKeepsTheDatabaseStorage(t *testing.T) {
	dir := t.TempDir()
	writeStorageRender(t, dir, config.Options{Namespace: "confighub", APINodePort: 32180, StorageClass: "auto-gp3", StorageSize: "20Gi"})

	o := Options{KubeContext: "prod", OutDir: dir}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "auto-gp3" || o.StorageSize != "20Gi" {
		t.Errorf("a re-run without the flags has StorageClass %q, StorageSize %q", o.StorageClass, o.StorageSize)
	}
	if got := o.deploymentOptions().StorageClass; got != "auto-gp3" {
		t.Errorf("the re-run renders StorageClass %q", got)
	}

	// A flag given now is what the operator asked for; the cluster's check
	// says whether it can be.
	o = Options{KubeContext: "prod", OutDir: dir, StorageClass: "gp3"}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "gp3" || o.StorageSize != "20Gi" {
		t.Errorf("--storage-class gp3 gave StorageClass %q, StorageSize %q", o.StorageClass, o.StorageSize)
	}

	// An install that named none keeps naming none.
	plain := t.TempDir()
	writeStorageRender(t, plain, config.Options{Namespace: "confighub", APINodePort: 32180})
	o = Options{KubeContext: "prod", OutDir: plain}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "" || o.StorageSize != config.DefaultStorageSize {
		t.Errorf("a re-run of a default install has StorageClass %q, StorageSize %q", o.StorageClass, o.StorageSize)
	}
}

// Moving to an external database leaves the old volume's class behind, rather
// than refusing it as a flag that does not apply.
func TestExternalDatabaseDropsTheRecordedStorage(t *testing.T) {
	dir := t.TempDir()
	writeStorageRender(t, dir, config.Options{Namespace: "confighub", APINodePort: 32180, StorageClass: "auto-gp3"})

	o := Options{KubeContext: "prod", OutDir: dir, Database: "external", DatabaseURL: "postgresql://db.example.com/confighub", APINodePort: 32180}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "" {
		t.Errorf("StorageClass %q kept for an external database", o.StorageClass)
	}
	if err := o.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// The package gets the class and size as inputs, and only when they say
// something, so a render that names neither works with a package from before
// they existed.
func TestInstallerArgsWithStorage(t *testing.T) {
	opts, s := installerOptions(t, false)
	args, err := installerArgs(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range flagValues(args, "--input") {
		if strings.HasPrefix(input, "storage_") {
			t.Errorf("passed %s with no storage flags", input)
		}
	}

	opts.StorageClass, opts.StorageSize = "auto-gp3", "20Gi"
	args, err = installerArgs(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	inputs := flagValues(args, "--input")
	for _, want := range []string{"storage_class=auto-gp3", "storage_size=20Gi"} {
		if !slices.Contains(inputs, want) {
			t.Errorf("no --input %s in %v", want, args)
		}
	}
}

func stubStorageClasses(t *testing.T, classes []storageClass, err error) *int {
	t.Helper()
	calls := 0
	old := listStorageClasses
	listStorageClasses = func(context.Context, kubeEnv) ([]storageClass, error) {
		calls++
		return classes, err
	}
	t.Cleanup(func() { listStorageClasses = old })
	return &calls
}

func contextInterviewOptions(t *testing.T) Options {
	return Options{
		Target: TargetContext, KubeContext: "prod", APINodePort: 32180, OCIURL: "https://oci.example.com",
		Database: string(config.DatabaseInternal), NewAdminKey: true, Image: "img:v1",
		OutDir: t.TempDir(),
	}
}

// On a cluster with no default StorageClass, the interview lists the ones it
// has and asks which the database uses.
func TestInterviewAsksForAStorageClassWithNoDefault(t *testing.T) {
	stubStorageClasses(t, []storageClass{
		{Name: "auto-gp3", Provisioner: "ebs.csi.eks.amazonaws.com"},
		{Name: "efs", Provisioner: "efs.csi.aws.com"},
	}, nil)
	o := contextInterviewOptions(t)
	var out strings.Builder
	if err := Interview(strings.NewReader("2\n"), &out, &o); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "efs" {
		t.Errorf("StorageClass = %q, want the one chosen", o.StorageClass)
	}
	for _, want := range []string{"no default StorageClass", "auto-gp3 (ebs.csi.eks.amazonaws.com)", "efs (efs.csi.aws.com)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the interview did not say %q:\n%s", want, out.String())
		}
	}
}

// With a default class, or a class already chosen, there is nothing to ask.
func TestInterviewSkipsTheStorageClass(t *testing.T) {
	stubStorageClasses(t, []storageClass{{Name: "gp2", Default: true}, {Name: "gp3"}}, nil)
	o := contextInterviewOptions(t)
	var out strings.Builder
	if err := Interview(strings.NewReader(""), &out, &o); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "" || strings.Contains(out.String(), "StorageClass") {
		t.Errorf("asked about a StorageClass on a cluster with a default (got %q):\n%s", o.StorageClass, out.String())
	}

	calls := stubStorageClasses(t, nil, nil)
	o = contextInterviewOptions(t)
	o.StorageClass = "gp3"
	if err := Interview(strings.NewReader(""), &strings.Builder{}, &o); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || o.StorageClass != "gp3" {
		t.Errorf("--storage-class gp3 was asked about again: %d lists, StorageClass %q", *calls, o.StorageClass)
	}

	// A re-run keeps the class its previous render names.
	o = contextInterviewOptions(t)
	writeStorageRender(t, o.OutDir, config.Options{Namespace: "confighub", APINodePort: 32180, StorageClass: "gp3"})
	if err := Interview(strings.NewReader(""), &strings.Builder{}, &o); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Errorf("a re-run with a recorded class listed the cluster's")
	}

	// An unreachable cluster is the install's to report.
	stubStorageClasses(t, nil, errors.New("connection refused"))
	o = contextInterviewOptions(t)
	if err := Interview(strings.NewReader(""), &strings.Builder{}, &o); err != nil {
		t.Errorf("an unreachable cluster failed the interview: %v", err)
	}
}

// A cluster with no StorageClass at all has nothing to list: the class is typed,
// and a blank answer stops rather than installing a database that cannot start.
func TestInterviewWithNoStorageClasses(t *testing.T) {
	stubStorageClasses(t, nil, nil)
	o := contextInterviewOptions(t)
	if err := Interview(strings.NewReader("Bad_Name\nauto-gp3\n"), &strings.Builder{}, &o); err != nil {
		t.Fatal(err)
	}
	if o.StorageClass != "auto-gp3" {
		t.Errorf("StorageClass = %q", o.StorageClass)
	}

	o = contextInterviewOptions(t)
	err := Interview(strings.NewReader("\n"), &strings.Builder{}, &o)
	if err == nil || !strings.Contains(err.Error(), "--storage-class") {
		t.Errorf("a blank answer: want an error naming --storage-class, got %v", err)
	}
}

func stubLiveClaim(t *testing.T, class, size string, exists bool) {
	t.Helper()
	old := liveDatabaseClaim
	liveDatabaseClaim = func(context.Context, kubeEnv, string) (string, string, bool, error) {
		return class, size, exists, nil
	}
	t.Cleanup(func() { liveDatabaseClaim = old })
}

// A StatefulSet's claim template cannot change, so a render that changes it is
// refused before anything is applied, saying how to get past it.
func TestRefuseDatabaseClaimChange(t *testing.T) {
	o := &Options{Namespace: "confighub", Database: string(config.DatabaseInternal), StorageClass: "auto-gp3", OutDir: "/out"}
	kube := kubeEnv{context: "prod"}

	stubLiveClaim(t, "", "10Gi", true)
	err := refuseDatabaseClaimChange(context.Background(), kube, o)
	if err == nil {
		t.Fatal("a class added to a live claim template was not refused")
	}
	for _, want := range []string{
		"10Gi from the cluster's default StorageClass",
		"10Gi from StorageClass auto-gp3",
		"kubectl --context prod -n confighub delete statefulset confighub-postgres",
		"kubectl --context prod -n confighub delete pvc data-confighub-postgres-0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}

	stubLiveClaim(t, "auto-gp3", "10Gi", true)
	if err := refuseDatabaseClaimChange(context.Background(), kube, o); err != nil {
		t.Errorf("the same claim was refused: %v", err)
	}

	stubLiveClaim(t, "", "", false)
	if err := refuseDatabaseClaimChange(context.Background(), kube, o); err != nil {
		t.Errorf("a first install was refused: %v", err)
	}

	o.StorageSize = "20Gi"
	stubLiveClaim(t, "auto-gp3", "10Gi", true)
	if err := refuseDatabaseClaimChange(context.Background(), kube, o); err == nil {
		t.Error("a resized claim template was not refused")
	}
}

// An install into a cluster with no default StorageClass, and none named, stops
// before anything is created, listing the classes and both ways past it.
func TestCheckStorageClassWithNoDefault(t *testing.T) {
	stubStorageClasses(t, []storageClass{
		{Name: "auto-gp3", Provisioner: "ebs.csi.eks.amazonaws.com"},
		{Name: "efs", Provisioner: "efs.csi.aws.com"},
	}, nil)
	o := &Options{Target: TargetContext, KubeContext: "eks", Database: string(config.DatabaseInternal)}
	err := checkStorageClass(context.Background(), UI{Out: io.Discard}, o)
	if err == nil {
		t.Fatal("no default StorageClass was not refused")
	}
	for _, want := range []string{
		`"eks" has no default StorageClass`,
		"auto-gp3 (ebs.csi.eks.amazonaws.com)",
		"efs (efs.csi.aws.com)",
		"--kube-context eks --storage-class auto-gp3",
		"--database=external --database-url <url>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}

	stubStorageClasses(t, nil, nil)
	err = checkStorageClass(context.Background(), UI{Out: io.Discard}, o)
	if err == nil || !strings.Contains(err.Error(), "no StorageClasses at all") {
		t.Errorf("a cluster with no StorageClasses: got %v", err)
	}
}

// Nothing to refuse with a default class, a class named, an external database,
// or a kind cluster, which always has a default.
func TestCheckStorageClassPasses(t *testing.T) {
	calls := stubStorageClasses(t, []storageClass{{Name: "gp2", Default: true}}, nil)
	o := &Options{Target: TargetContext, KubeContext: "eks", Database: string(config.DatabaseInternal)}
	if err := checkStorageClass(context.Background(), UI{Out: io.Discard}, o); err != nil {
		t.Errorf("a cluster with a default: %v", err)
	}

	stubStorageClasses(t, nil, nil)
	for name, o := range map[string]*Options{
		"--storage-class":     {Target: TargetContext, KubeContext: "eks", Database: string(config.DatabaseInternal), StorageClass: "auto-gp3"},
		"--database=external": {Target: TargetContext, KubeContext: "eks", Database: string(config.DatabaseExternal)},
		"kind":                {Target: TargetKind, ClusterName: "confighub", Database: string(config.DatabaseInternal)},
	} {
		if err := checkStorageClass(context.Background(), UI{Out: io.Discard}, o); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if *calls != 1 {
		t.Errorf("listed StorageClasses %d times; only the first case needs to", *calls)
	}
}

// A cluster whose StorageClasses can't be read may well have a default: the
// install warns, says what to do if it has none, and goes on.
func TestCheckStorageClassUnreadable(t *testing.T) {
	stubStorageClasses(t, nil, errors.New("kubectl get storageclass: exit status 1\n"+
		`Error from server (Forbidden): storageclasses.storage.k8s.io is forbidden`))
	o := &Options{Target: TargetContext, KubeContext: "eks", Database: string(config.DatabaseInternal), DryRun: true}
	var out strings.Builder
	if err := checkStorageClass(context.Background(), UI{Out: &out}, o); err != nil {
		t.Fatalf("an unreadable cluster was refused: %v", err)
	}
	for _, want := range []string{"Forbidden", "--storage-class"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the warning does not say %q:\n%s", want, out.String())
		}
	}
}
