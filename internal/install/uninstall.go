package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Uninstall removes what Run created.
//
// Deliberately not symmetric with install in one respect: it does not touch the
// administrator's private key. That is a credential the operator holds, not
// deployment state this tool owns, and deleting credentials as a side effect of
// removing infrastructure is the kind of helpfulness nobody wants.
func Uninstall(ctx context.Context, u UI, o *Options, keepConfig bool) error {
	if err := o.Defaults(); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}

	switch o.Target {
	case TargetKind:
		exists, err := kindClusterExists(o.ClusterName)
		if err != nil {
			return err
		}
		if !exists {
			u.detail("no kind cluster named %q", o.ClusterName)
			break
		}
		u.step("Deleting the cluster %q", o.ClusterName)
		kubeconfig := filepath.Join(o.OutDir, "kubeconfig")
		if err := deleteKindCluster(o.ClusterName, kubeconfig); err != nil {
			return err
		}

	case TargetContext:
		if err := toolKubectl.require(); err != nil {
			return err
		}
		u.step("Deleting namespace %q from context %q", o.Namespace, o.KubeContext)
		kube := kubeEnv{context: o.KubeContext}
		// --ignore-not-found so a re-run after a partial teardown succeeds
		// rather than failing on the half that is already gone.
		if _, err := run(ctx, toolKubectl.name, kube.args(
			"delete", "namespace", o.Namespace, "--ignore-not-found",
		)...); err != nil {
			return err
		}
	}

	if keepConfig {
		u.detail("keeping the generated config in %s", o.OutDir)
		u.detail("a reinstall will reuse it, and be the same instance")
		return nil
	}

	u.step("Removing the generated config")
	if err := os.RemoveAll(o.OutDir); err != nil {
		return fmt.Errorf("removing %s: %w", o.OutDir, err)
	}
	u.detail("removed %s", o.OutDir)
	u.detail("the administrator key in cub's key directory was left alone")
	return nil
}
