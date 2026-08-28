package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// Target is where the instance is installed.
type Target string

const (
	// TargetKind creates a local cluster. The only prerequisite is Docker, and
	// nothing about the machine has to be true beforehand -- no cluster, no
	// kubeconfig, no DNS.
	TargetKind Target = "kind"

	// TargetContext installs into a cluster that already exists, named by a
	// context in the caller's kubeconfig.
	TargetContext Target = "context"
)

// Defaults that are chosen rather than inherited, gathered so they are visible
// in one place instead of spread across flag declarations.
const (
	DefaultClusterName = "confighub"
	DefaultNamespace   = "confighub"

	// DefaultAPINodePort and DefaultOCINodePort sit inside Kubernetes' NodePort
	// range. They are fixed rather than allocated because kind has to publish
	// them on the host at cluster-creation time, before anything exists to ask.
	DefaultAPINodePort = 32180
	DefaultOCINodePort = 32181

	// DefaultAdminKeyName is the alias the administrator's private key is stored
	// under in cub's key directory, where `cub auth login --private-key` finds
	// it by that name.
	DefaultAdminKeyName = "confighub-admin"
)

// Options is everything `cub server install` needs. Every field is settable by
// a flag; interactive mode fills the same struct by asking, so the two paths
// cannot drift apart.
type Options struct {
	Target      Target
	ClusterName string // kind cluster to create (TargetKind)
	KubeContext string // existing context to install into (TargetContext)

	Namespace string
	Image     string

	Database    string // "internal" or "external"
	DatabaseURL string

	APINodePort int
	OCINodePort int

	AdminKeyName string

	// NewAdminKey forces a fresh administrator keypair even when one of that
	// name is already in cub's key store.
	//
	// The default is to reuse, because uninstall leaves the key behind on
	// purpose -- a credential is not deployment state -- so reinstalling should
	// keep working with the key you already hold.
	NewAdminKey bool

	// OutDir is where the generated manifests are written. They are kept rather
	// than discarded because they are the record of what was installed, and
	// because re-running reads them back to avoid rotating generated secrets.
	OutDir string

	// DryRun generates and writes everything without touching a cluster.
	DryRun bool

	// SkipAuth leaves the instance running without logging in. For CI, which
	// often wants the key rather than a session.
	SkipAuth bool
}

// Defaults fills in what the caller did not set.
func (o *Options) Defaults() error {
	if o.Target == "" {
		o.Target = TargetKind
	}
	if o.ClusterName == "" {
		o.ClusterName = DefaultClusterName
	}
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if o.Database == "" {
		o.Database = string(config.DatabaseInternal)
	}
	if o.APINodePort == 0 {
		o.APINodePort = DefaultAPINodePort
	}
	if o.OCINodePort == 0 {
		o.OCINodePort = DefaultOCINodePort
	}
	if o.AdminKeyName == "" {
		o.AdminKeyName = DefaultAdminKeyName
	}
	if o.OutDir == "" {
		dir, err := defaultOutDir(o.instanceName())
		if err != nil {
			return err
		}
		o.OutDir = dir
	}
	return nil
}

// instanceName is what this install is called on disk. The cluster name for a
// kind install; the context name otherwise, so two installs into two clusters do
// not overwrite each other's generated secrets.
func (o *Options) instanceName() string {
	if o.Target == TargetContext && o.KubeContext != "" {
		return sanitize(o.KubeContext)
	}
	return sanitize(o.ClusterName)
}

// defaultOutDir puts the generated config beside cub's own configuration, since
// that is already the directory holding the private key this install produces.
func defaultOutDir(instance string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine a home directory for the generated config: %w", err)
	}
	return filepath.Join(home, ".confighub", "servers", instance), nil
}

// sanitize keeps a name usable as a single path segment. Kubernetes context
// names routinely contain slashes and colons (an EKS ARN, for one).
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
}

// Validate rejects combinations that cannot produce a working install, before
// anything is created.
func (o *Options) Validate() error {
	switch o.Target {
	case TargetKind:
		if o.KubeContext != "" {
			return fmt.Errorf("--kube-context applies to an existing cluster; pass --target=context to install into one")
		}
	case TargetContext:
		if o.KubeContext == "" {
			return fmt.Errorf("--target=context needs the context to install into: pass --kube-context")
		}
	default:
		return fmt.Errorf("unknown target %q: expected kind or context", o.Target)
	}

	// The rest is the generator's own contract, checked by the same code that
	// will check it again at Build time. Calling it here means a bad combination
	// is reported before a cluster is created rather than after.
	scOpts := o.deploymentOptions()
	return scOpts.Validate()
}

// deploymentOptions projects onto the generator's option struct.
//
// The two are deliberately separate: this one describes an install (which
// cluster, whether to create it, whether to log in), and the generator's
// describes a deployment. Only the overlap crosses over.
func (o *Options) deploymentOptions() config.Options {
	opts := config.Options{
		Namespace:   o.Namespace,
		Image:       o.Image,
		Database:    config.DatabaseMode(o.Database),
		DatabaseURL: o.DatabaseURL,
		Ingress:     config.IngressNone,
		APINodePort: o.APINodePort,
		OCINodePort: o.OCINodePort,
	}
	opts.Defaults(defaultImage())
	return opts
}

// APIURL is where the instance answers once it is up.
//
// A NodePort published on the host by kind, so this is a real URL that survives
// the terminal closing -- not a port-forward that has to stay running.
func (o *Options) APIURL() string {
	return fmt.Sprintf("http://localhost:%d", o.APINodePort)
}
