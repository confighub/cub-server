package install

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// listStorageClasses reads the cluster's StorageClasses, by name. A variable so
// that the interview can be tested without a cluster.
var listStorageClasses = func(ctx context.Context, kube kubeEnv) ([]storageClass, error) {
	out, err := run(ctx, toolKubectl.name, kube.args("get", "storageclass", "-o", "json")...)
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

// liveDatabaseClaim reads the StorageClass and size of the claim template of the
// bundled database's StatefulSet in the cluster, and whether there is one. A
// variable so that the check can be tested without a cluster.
var liveDatabaseClaim = func(ctx context.Context, kube kubeEnv, namespace string) (class, size string, exists bool, err error) {
	out, err := run(ctx, toolKubectl.name, kube.args(
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
func refuseDatabaseClaimChange(ctx context.Context, kube kubeEnv, o *Options) error {
	if o.Database != string(config.DatabaseInternal) {
		return nil
	}
	liveClass, liveSize, exists, err := liveDatabaseClaim(ctx, kube, o.Namespace)
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
	claim := "data-" + databaseStatefulSet + "-0"
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
		kubeFlags(kube), o.Namespace, claim,
		kubeFlags(kube), o.Namespace, databaseStatefulSet,
		kubeFlags(kube), o.Namespace, claim,
		o.OutDir)
}

// kubeFlags echoes the kubeconfig and context a command runs against, so a
// printed kubectl command can be pasted as it is.
func kubeFlags(kube kubeEnv) string {
	args := kube.args()
	if len(args) == 0 {
		return ""
	}
	return " " + strings.Join(args, " ")
}
