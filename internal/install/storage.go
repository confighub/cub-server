package install

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/confighub/cub-server/internal/config"
)

// The bundled database's volume.
//
// Its claim comes from the StatefulSet's volume claim template, which names a
// StorageClass only when --storage-class gives one; otherwise the cluster's
// default class provisions it. kind has a default class. Many production
// clusters do not -- platform teams that want every workload to choose its
// storage, and EKS Auto Mode, which ships with none -- and there the claim
// stays Pending and the database never starts.
//
// This never creates a StorageClass. One is cluster-scoped and specific to a
// provisioner, and on the clusters that have no default, choosing it is the
// platform team's call.

// defaultClassAnnotation marks a cluster's default StorageClass.
const defaultClassAnnotation = "storageclass.kubernetes.io/is-default-class"

// storageClass is one of a cluster's StorageClasses.
type storageClass struct {
	Name        string
	Provisioner string
	Default     bool
}

// storageCluster is what the storage checks and the database's wait read from
// the cluster. kubeEnv reads it with kubectl; the tests pass a fake.
type storageCluster interface {
	// storageClasses lists the cluster's StorageClasses, by name.
	storageClasses(ctx context.Context) ([]storageClass, error)
	// databaseClaimTemplate reads the StorageClass and size of the bundled
	// database's StatefulSet's claim template, and whether there is one.
	databaseClaimTemplate(ctx context.Context, namespace string) (class, size string, exists bool, err error)
	// claim reads a claim and its events, or nil when there is no such claim.
	claim(ctx context.Context, namespace, name string) (*claimStatus, error)
	// waitForRollout blocks until a workload's rollout completes.
	waitForRollout(ctx context.Context, namespace, kind, name string, timeout time.Duration) error
	// commandFlags is the kubectl flags that reach the cluster, for commands
	// printed to paste.
	commandFlags() string
}

// storageClasses reads the cluster's StorageClasses, by name.
func (k kubeEnv) storageClasses(ctx context.Context) ([]storageClass, error) {
	out, err := run(ctx, toolKubectl.name, k.args("get", "storageclass", "-o", "json")...)
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Provisioner string `json:"provisioner"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("reading the cluster's StorageClasses: %w", err)
	}
	classes := make([]storageClass, 0, len(list.Items))
	for _, item := range list.Items {
		classes = append(classes, storageClass{
			Name:        item.Metadata.Name,
			Provisioner: item.Provisioner,
			Default:     item.Metadata.Annotations[defaultClassAnnotation] == "true",
		})
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i].Name < classes[j].Name })
	return classes, nil
}

// hasDefaultClass reports whether any of the classes is the cluster's default.
func hasDefaultClass(classes []storageClass) bool {
	for _, c := range classes {
		if c.Default {
			return true
		}
	}
	return false
}

// checkStorageClass refuses to install the bundled database into a cluster that
// has no default StorageClass, when --storage-class does not name one: its
// claim would stay Pending, the database would never start, and the install
// would time out waiting for it with nothing saying why.
//
// Only for a cluster of the operator's own; a kind cluster always has a
// default. Checked with --dry-run too, since a render applied by hand to such a
// cluster fails the same way. A cluster whose StorageClasses can't be read --
// unreachable from here, or a kubeconfig whose RBAC stops at the namespace --
// is warned about rather than refused: it may well have a default.
func checkStorageClass(ctx context.Context, u UI, o *Options, cluster storageCluster) error {
	if o.Target != TargetContext || o.Database != string(config.DatabaseInternal) || o.StorageClass != "" {
		return nil
	}
	classes, err := cluster.storageClasses(ctx)
	if err != nil {
		u.warn("could not list the StorageClasses of %q, so not checking it has a default one:", o.KubeContext)
		u.warn("  %s", lastLine(err.Error()))
		u.warn("without one, the database's volume claim stays Pending; pass --storage-class to name a class")
		return nil
	}
	for _, c := range classes {
		if c.Default {
			u.detail("storage: %s, the cluster's default StorageClass", c.Name)
			return nil
		}
	}

	var lines []string
	if len(classes) == 0 {
		lines = append(lines, "    It has no StorageClasses at all; ask whoever runs it which to use.")
	} else {
		lines = append(lines, "    Its StorageClasses:")
		for _, c := range classes {
			lines = append(lines, fmt.Sprintf("      %s (%s)", c.Name, c.Provisioner))
		}
	}
	example := "<name>"
	if len(classes) > 0 {
		example = classes[0].Name
	}
	return fmt.Errorf(
		"the cluster %q has no default StorageClass, so the bundled database's volume claim\n"+
			"    would stay Pending and the database would never start.\n"+
			"%s\n\n"+
			"    Name the class its volume comes from:\n"+
			"      cub server install --kube-context %s --storage-class %s\n\n"+
			"    Or bring your own database, such as RDS, and need no volume here:\n"+
			"      cub server install --kube-context %s --database=external --database-url <url>",
		o.KubeContext, strings.Join(lines, "\n"), o.KubeContext, example, o.KubeContext)
}

// lastLine is the last line of s: of an error from run, kubectl's own message,
// such as a Forbidden, after the command line that failed.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	return s[strings.LastIndex(s, "\n")+1:]
}

// databaseClaimTemplate reads the StorageClass and size of the claim template of
// the bundled database's StatefulSet in the cluster, and whether there is one.
func (k kubeEnv) databaseClaimTemplate(ctx context.Context, namespace string) (class, size string, exists bool, err error) {
	out, err := run(ctx, toolKubectl.name, k.args(
		"-n", namespace, "get", "statefulset", databaseStatefulSet, "-o", "json", "--ignore-not-found")...)
	if err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(out) == "" {
		return "", "", false, nil
	}
	var sts claimTemplate
	if err := json.Unmarshal([]byte(out), &sts); err != nil {
		return "", "", false, fmt.Errorf("reading statefulset %s/%s: %w", namespace, databaseStatefulSet, err)
	}
	class, size = sts.classAndSize()
	return class, size, true, nil
}

// refuseDatabaseClaimChange refuses to apply a render whose database claim asks
// for a different StorageClass or size than the StatefulSet already in the
// cluster.
//
// The API server refuses it anyway -- a StatefulSet's claim templates cannot
// change -- but after everything before it in the apply order has been applied,
// and in a message about the StatefulSet's spec rather than about the flag. The
// likeliest way here is the one --storage-class exists for: an install whose
// database never started, because the cluster has no default StorageClass,
// re-run with one.
func refuseDatabaseClaimChange(ctx context.Context, cluster storageCluster, o *Options) error {
	if o.Database != string(config.DatabaseInternal) {
		return nil
	}
	liveClass, liveSize, exists, err := cluster.databaseClaimTemplate(ctx, o.Namespace)
	if err != nil || !exists {
		return err
	}
	opts := o.deploymentOptions()
	if liveClass == opts.StorageClass && (liveSize == "" || liveSize == opts.StorageSize) {
		return nil
	}

	describe := func(class, size string) string {
		if class == "" {
			class = "the cluster's default StorageClass"
		} else {
			class = "StorageClass " + class
		}
		return size + " from " + class
	}
	k := cluster.commandFlags()
	return fmt.Errorf(
		"the bundled database's volume is %s, and this install asks for %s.\n"+
			"    A StatefulSet's volume claim template cannot be changed in place.\n\n"+
			"    If the database never started -- its claim is Pending, as it is on a cluster\n"+
			"    with no default StorageClass -- there is no data to lose. Delete both and re-run:\n"+
			"      kubectl%s -n %s get pvc %s\n"+
			"      kubectl%s -n %s delete statefulset %s\n"+
			"      kubectl%s -n %s delete pvc %s\n\n"+
			"    If it holds data, keep the volume it has: re-run with --storage-class and\n"+
			"    --storage-size matching it, or with no flag to keep what %s recorded.",
		describe(liveClass, liveSize), describe(opts.StorageClass, opts.StorageSize),
		k, o.Namespace, databaseClaim,
		k, o.Namespace, databaseStatefulSet,
		k, o.Namespace, databaseClaim,
		o.OutDir)
}

// commandFlags echoes the kubeconfig and context a command runs against, so a
// printed kubectl command can be pasted as it is.
func (k kubeEnv) commandFlags() string {
	args := k.args()
	if len(args) == 0 {
		return ""
	}
	return " " + strings.Join(args, " ")
}
