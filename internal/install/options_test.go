package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/confighub/sdk/core/serverconfig"
	"github.com/confighub/sdk/core/serverconfig/k8s"
)

func TestDefaultsFillEverythingNeededToInstall(t *testing.T) {
	o := &Options{}
	if err := o.Defaults(); err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if err := o.Validate(); err != nil {
		t.Fatalf("a defaulted Options should be installable, got: %v", err)
	}
	if o.Target != TargetKind {
		t.Errorf("target = %q, want %q", o.Target, TargetKind)
	}
	if o.OutDir == "" || !filepath.IsAbs(o.OutDir) {
		t.Errorf("OutDir = %q, want an absolute path", o.OutDir)
	}
	if o.APIURL() != "http://localhost:32180" {
		t.Errorf("APIURL = %q", o.APIURL())
	}
}

func TestDefaultsDoNotOverrideWhatWasSet(t *testing.T) {
	o := &Options{Namespace: "mine", APINodePort: 31000, ClusterName: "other"}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.Namespace != "mine" || o.APINodePort != 31000 || o.ClusterName != "other" {
		t.Errorf("Defaults overwrote a caller's values: %+v", o)
	}
}

func TestValidateRejectsUnworkableCombinations(t *testing.T) {
	tests := []struct {
		name string
		o    Options
		want string
	}{
		{
			name: "context target with no context",
			o:    Options{Target: TargetContext},
			want: "--kube-context",
		},
		{
			name: "kind target with a context",
			o:    Options{Target: TargetKind, KubeContext: "somewhere"},
			want: "--target=context",
		},
		{
			name: "unknown target",
			o:    Options{Target: "vm"},
			want: "unknown target",
		},
		{
			name: "external database with no url",
			o:    Options{Database: string(serverconfig.DatabaseExternal)},
			want: "--database-url",
		},
		{
			name: "node ports collide",
			o:    Options{APINodePort: 32100, OCINodePort: 32100},
			want: "cannot be the same port",
		},
		{
			name: "node port outside the range",
			o:    Options{APINodePort: 8080},
			want: "NodePort range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := tt.o
			if err := o.Defaults(); err != nil {
				t.Fatal(err)
			}
			err := o.Validate()
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should mention %q, got: %v", tt.want, err)
			}
		})
	}
}

// The instance name becomes a directory name, and a kube context is routinely an
// ARN full of slashes and colons.
func TestInstanceNameIsUsableAsOnePathSegment(t *testing.T) {
	o := &Options{
		Target:      TargetContext,
		KubeContext: "arn:aws:eks:us-west-2:123456789012:cluster/prod",
	}
	name := o.instanceName()
	if strings.ContainsAny(name, `/\:`) {
		t.Errorf("instanceName = %q, still contains a path separator", name)
	}
	if name == "" {
		t.Error("instanceName is empty")
	}
}

// Two installs into two clusters must not share an output directory, or the
// second would read the first's generated secrets and think they were its own.
func TestInstanceNameSeparatesInstalls(t *testing.T) {
	a := (&Options{Target: TargetKind, ClusterName: "one"}).instanceName()
	b := (&Options{Target: TargetKind, ClusterName: "two"}).instanceName()
	if a == b {
		t.Errorf("two clusters produced the same instance name %q", a)
	}
}

// readPrior is what makes re-running safe: a fresh signing key would log every
// session out, and a fresh worker master secret would break every enrolled
// worker. It reads the previous run's own output, so this round-trips through a
// real render.
func TestReadPriorRoundTripsGeneratedValues(t *testing.T) {
	dir := t.TempDir()

	opts := serverconfig.Options{Namespace: "confighub", APINodePort: 32180}
	opts.Defaults("ghcr.io/confighubai/confighub:test")
	surface, err := serverconfig.Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := k8s.Render(surface, opts)
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

	prior, adminJWK, err := readPrior(dir)
	if err != nil {
		t.Fatalf("readPrior: %v", err)
	}

	for _, key := range []string{"JWT_PRIVATE_KEY_JWK", "WORKER_MASTER_SECRET", "POSTGRES_PASSWORD"} {
		if prior[key] == "" {
			t.Errorf("%s was not recovered; re-running would rotate it", key)
		}
		if prior[key] != surface.Get(key) {
			t.Errorf("%s round-tripped to a different value", key)
		}
	}
	if adminJWK == "" {
		t.Error("the administrator public key was not recovered")
	}

	// The point of recovering them: a second Build reuses rather than rotates.
	second, err := serverconfig.Build(opts, prior)
	if err != nil {
		t.Fatal(err)
	}
	if second.Get("WORKER_MASTER_SECRET") != surface.Get("WORKER_MASTER_SECRET") {
		t.Error("re-running rotated the worker master secret")
	}
}

// A first run has nothing to read back, and that is not an error.
func TestReadPriorOnAnEmptyDirectory(t *testing.T) {
	prior, adminJWK, err := readPrior(t.TempDir())
	if err != nil {
		t.Fatalf("a first run should not error: %v", err)
	}
	if len(prior) != 0 || adminJWK != "" {
		t.Errorf("expected nothing recovered, got %d values and adminJWK=%q", len(prior), adminJWK)
	}
}
