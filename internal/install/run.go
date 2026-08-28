package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/confighub/sdk/core/serverconfig"
	"github.com/confighub/sdk/core/serverconfig/k8s"
	"gopkg.in/yaml.v3"
)

const (
	rolloutTimeout = 5 * time.Minute
	readyTimeout   = 3 * time.Minute
)

// Run performs an install. It is idempotent: every step either creates what is
// missing or reports what is already there, so re-running after a failure
// resumes rather than starting over or duplicating.
func Run(ctx context.Context, u UI, o *Options) error {
	if err := o.Defaults(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}

	if err := preflight(ctx, u, o); err != nil {
		return err
	}

	files, keyPath, err := generate(u, o)
	if err != nil {
		return err
	}

	if o.DryRun {
		u.section("Dry run: nothing was created in a cluster.",
			"manifests   "+o.OutDir,
			"key         "+keyPath,
			"",
			"Re-run without --dry-run to install this exact configuration.",
		)
		return nil
	}

	kube, err := provisionCluster(ctx, u, o)
	if err != nil {
		return err
	}

	if err := applyManifests(ctx, u, o, kube, files); err != nil {
		return err
	}

	if err := awaitReady(ctx, u, o, kube); err != nil {
		return err
	}

	return authenticate(ctx, u, o, keyPath)
}

// preflight checks what has to be true before anything is created, so that a
// missing prerequisite costs a message rather than a half-built cluster.
func preflight(ctx context.Context, u UI, o *Options) error {
	u.step("Checking prerequisites")

	if o.Target == TargetKind {
		if err := toolDocker.require(); err != nil {
			return fmt.Errorf("%w", err)
		}
		if _, err := run(ctx, toolDocker.name, "info"); err != nil {
			return fmt.Errorf("Docker is installed but not responding. Start Docker and try again")
		}
		u.detail("docker: running")

		if err := toolKind.require(); err != nil {
			return err
		}
		u.detail("kind: found")
	}

	if err := toolKubectl.require(); err != nil {
		return err
	}
	u.detail("kubectl: found")
	return nil
}

// generate resolves the config surface, renders the manifests to disk, and
// stores the administrator's private key. It returns the rendered files and the
// path to that key.
//
// The two halves of the keypair are persisted together, in this one step,
// because that is the only moment both exist. Writing the public half into the
// ConfigMap and leaving the private half to a later step means any early return
// between them -- a dry run, a failed cluster create, a interrupted rollout --
// leaves an instance configured to trust a key nobody holds. Nothing can
// recover from that: only the holder ever had the private half.
func generate(u UI, o *Options) ([]k8s.File, string, error) {
	u.step("Generating configuration")

	prior, priorAdminJWK, err := readPrior(o.OutDir)
	if err != nil {
		return nil, "", err
	}

	opts := o.serverconfigOptions()
	if priorAdminJWK != "" {
		// Replacing this would register a new key at the next start and strand
		// the private key the operator already holds.
		opts.AdminPublicJWK = priorAdminJWK
		u.detail("keeping the administrator key from the previous run")
	}

	surface, err := serverconfig.Build(opts, prior)
	if err != nil {
		return nil, "", err
	}

	files, err := k8s.Render(surface, opts)
	if err != nil {
		return nil, "", err
	}

	for _, f := range files {
		path := filepath.Join(o.OutDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, "", err
		}
		mode := os.FileMode(0o644)
		if f.Sensitive {
			mode = 0o600
		}
		if err := os.WriteFile(path, f.Content, mode); err != nil {
			return nil, "", err
		}
	}
	u.detail("wrote %d files to %s", len(files), o.OutDir)
	u.detail("image: %s", opts.Image)

	keyPath, err := persistAdminKey(u, o, surface)
	if err != nil {
		return nil, "", err
	}
	return files, keyPath, nil
}

// persistAdminKey stores the private half of a freshly generated keypair, or
// locates the existing one when this run reused a public key.
//
// An empty path with no error means the public key was reused but its private
// half is not in cub's key store -- the operator moved or deleted it. The
// install can still proceed; only the sign-in cannot.
func persistAdminKey(u UI, o *Options, surface *serverconfig.Surface) (string, error) {
	if surface.AdminKey == nil {
		path, exists := keyExists(o.AdminKeyName)
		if exists {
			u.detail("using the existing administrator key %s", path)
			return path, nil
		}

		// This run reused a public key from a previous run, and the matching
		// private half is not here. The instance that would be installed is one
		// nobody can log into, and nothing can recover it: only the holder ever
		// had the private half.
		//
		// Refused rather than warned, and refused here -- before a cluster is
		// created -- because the alternative is several minutes of work ending
		// in a dead end. --skip-auth means the caller is managing credentials
		// themselves, so it is theirs to decide.
		if o.SkipAuth {
			u.warn("no private key named %q; --skip-auth, so continuing", o.AdminKeyName)
			return "", nil
		}
		return "", fmt.Errorf(
			"%s configures an administrator whose private key is not in cub's key store (expected %q).\n"+
				"    Nothing can recover it: only the holder ever had the private half.\n\n"+
				"    To start over with a fresh administrator key:\n"+
				"      cub server uninstall%s\n"+
				"      cub server install%s\n\n"+
				"    Or pass --skip-auth to install anyway and sort out credentials yourself.",
			o.OutDir, o.AdminKeyName, outDirFlag(o), outDirFlag(o))
	}

	if path, exists := keyExists(o.AdminKeyName); exists {
		// The generator minted a key and the store already holds one under this
		// name. Writing would be refused, and overwriting would strand whatever
		// the existing one belongs to.
		return "", fmt.Errorf(
			"a key named %q already exists at %s, but this run generated a new one.\n"+
				"    Pass --admin-key-name to store this instance's key under a different name,\n"+
				"    or remove that file if it is no longer needed",
			o.AdminKeyName, path)
	}

	path, err := writeAdminKey(o.AdminKeyName, surface.AdminKey.PrivateJWK)
	if err != nil {
		return "", err
	}
	u.detail("wrote the administrator key to %s", path)
	return path, nil
}

// outDirFlag echoes back a non-default --out-dir, so the recovery commands this
// package prints can be pasted rather than adjusted.
func outDirFlag(o *Options) string {
	def, err := defaultOutDir(o.instanceName())
	if err == nil && o.OutDir == def {
		return ""
	}
	return " --out-dir " + o.OutDir
}

// readPrior recovers what an earlier run generated, from its own output.
//
// The generated files are the state. A separate state file would be a second
// thing to lose, and these values are already sitting in the Secret in a form
// meant to be read. A missing or unparseable previous run is not an error:
// generating fresh is correct for a first run.
func readPrior(outDir string) (serverconfig.Preserved, string, error) {
	prior := serverconfig.Preserved{}

	secretPath := filepath.Join(outDir, k8s.SecretsDir, "confighub-secret.yaml")
	if data, err := os.ReadFile(secretPath); err == nil {
		var doc struct {
			StringData map[string]string `yaml:"stringData"`
		}
		if yaml.Unmarshal(data, &doc) == nil {
			for k, v := range doc.StringData {
				prior[k] = v
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}

	var adminJWK string
	cmPath := filepath.Join(outDir, k8s.ConfigDir, "20-configmap.yaml")
	if data, err := os.ReadFile(cmPath); err == nil {
		var doc struct {
			Data map[string]string `yaml:"data"`
		}
		if yaml.Unmarshal(data, &doc) == nil {
			adminJWK = doc.Data[serverconfig.AdminJWKEnv]
		}
	} else if !os.IsNotExist(err) {
		return nil, "", err
	}

	return prior, adminJWK, nil
}

// provisionCluster returns the kube environment to install into, creating a kind
// cluster first when that is the target.
func provisionCluster(ctx context.Context, u UI, o *Options) (kubeEnv, error) {
	if o.Target == TargetContext {
		u.step("Using existing cluster")
		u.detail("context: %s", o.KubeContext)
		return kubeEnv{context: o.KubeContext}, nil
	}

	u.step("Preparing the cluster")
	kubeconfig, err := createKindCluster(ctx, u, o)
	if err != nil {
		return kubeEnv{}, err
	}
	u.detail("kubeconfig: %s", kubeconfig)
	return kubeEnv{kubeconfig: kubeconfig}, nil
}

// applyManifests applies the rendered files in their numbered order.
func applyManifests(ctx context.Context, u UI, o *Options, kube kubeEnv, files []k8s.File) error {
	u.step("Installing ConfigHub")

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, filepath.Join(o.OutDir, f.Path))
	}
	// The filenames carry their own apply order (00-namespace before the things
	// inside it, the Secret before the Deployment that reads it). Sorting by
	// path preserves it without restating the sequence here.
	sort.Strings(paths)

	if err := kube.applyFiles(ctx, paths); err != nil {
		return err
	}
	u.detail("applied %d manifests to namespace %q", len(paths), o.Namespace)
	return nil
}

// awaitReady blocks until the instance is actually serving.
func awaitReady(ctx context.Context, u UI, o *Options, kube kubeEnv) error {
	u.step("Waiting for it to come up")

	if o.Database == string(serverconfig.DatabaseInternal) {
		u.detail("database...")
		if err := kube.waitForRollout(ctx, o.Namespace, "statefulset", "confighub-postgres", rolloutTimeout); err != nil {
			return err
		}
	}

	u.detail("server...")
	if err := kube.waitForRollout(ctx, o.Namespace, "deployment", "confighub", rolloutTimeout); err != nil {
		return err
	}

	u.detail("api at %s...", o.APIURL())
	return waitForAPI(ctx, o.APIURL(), readyTimeout)
}

// authenticate points cub at the new instance and signs in, so the command ends
// with the caller able to use what it built rather than with instructions.
func authenticate(ctx context.Context, u UI, o *Options, keyPath string) error {
	u.step("Signing in")

	if keyPath == "" {
		// generate already explained why, in detail. Do not repeat it.
		return reportSuccess(u, o, "", false)
	}

	if o.SkipAuth {
		u.detail("--skip-auth: leaving the instance running without signing in")
		return reportSuccess(u, o, keyPath, false)
	}

	cub, err := cubBinary()
	if err != nil {
		u.warn("cub is not on PATH, so this last step is yours to run")
		return reportSuccess(u, o, keyPath, false)
	}

	if err := login(ctx, cub, o.APIURL(), o.AdminKeyName); err != nil {
		return err
	}
	if err := verify(ctx, cub); err != nil {
		return err
	}
	if name := activeContextName(); name != "" {
		u.detail("signed in; cub context %q now points at %s", name, o.APIURL())
	} else {
		u.detail("signed in as the local administrator")
	}
	return reportSuccess(u, o, keyPath, true)
}

func reportSuccess(u UI, o *Options, keyPath string, authenticated bool) error {
	lines := []string{
		"URL          " + o.APIURL(),
		"namespace    " + o.Namespace,
		"config       " + o.OutDir,
	}
	if keyPath != "" {
		lines = append(lines, "key          "+keyPath)
	}
	u.section("ConfigHub is running.", lines...)

	if authenticated {
		u.section("You are signed in. Next:",
			"cub space list",
			"cub auth browser-session      # open the same session in a browser",
		)
	} else {
		lines := append([]string{}, manualLoginInstructions(o.APIURL(), o.AdminKeyName)...)
		u.section("To sign in:", lines...)
	}

	u.section("The Secret is not committable:",
		filepath.Join(o.OutDir, k8s.SecretsDir),
	)
	return nil
}
