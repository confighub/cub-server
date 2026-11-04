package install

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/confighub/cub-server/internal/config"
)

// stubWait replaces the rollout and the claim reads for one test. The rollout
// blocks until its context is cancelled, or returns result after delay.
func stubWait(t *testing.T, result error, delay time.Duration, claim func(n int) *claimStatus) (cancelled *atomic.Bool) {
	t.Helper()
	cancelled = &atomic.Bool{}
	oldRollout, oldRead, oldInterval := rollout, readClaim, claimCheckInterval
	rollout = func(ctx context.Context, _ kubeEnv, _, _, _ string, _ time.Duration) error {
		select {
		case <-ctx.Done():
			cancelled.Store(true)
			return ctx.Err()
		case <-time.After(delay):
			return result
		}
	}
	var reads atomic.Int32
	readClaim = func(context.Context, kubeEnv, string, string) (*claimStatus, error) {
		return claim(int(reads.Add(1))), nil
	}
	claimCheckInterval = time.Millisecond
	t.Cleanup(func() { rollout, readClaim, claimCheckInterval = oldRollout, oldRead, oldInterval })
	return cancelled
}

func claimTestOptions() *Options {
	return &Options{Target: TargetContext, KubeContext: "eks", Namespace: "confighub", Database: string(config.DatabaseInternal)}
}

// As a kind cluster with its default class removed reports it: FailedBinding is
// a Normal event, not a Warning.
var noClassNoDefault = &claimStatus{Phase: "Pending", Events: []claimEvent{{
	Type: "Normal", Reason: "FailedBinding", Count: 12,
	Message: "no persistent volumes available for this claim and no storage class is set",
}}}

// A claim whose events say it will never bind ends the wait then, with the
// claim's own reason, rather than five minutes later with kubectl's timeout.
func TestAwaitDatabaseStopsOnAClaimThatWontBind(t *testing.T) {
	cancelled := stubWait(t, nil, time.Hour, func(n int) *claimStatus {
		if n < 3 {
			return &claimStatus{Phase: "Pending"} // just created: no events yet
		}
		return noClassNoDefault
	})
	err := awaitDatabase(context.Background(), claimTestOptions(), kubeEnv{context: "eks"})
	if err == nil {
		t.Fatal("a claim that won't bind did not end the wait")
	}
	if !cancelled.Load() {
		t.Error("the rollout wait was left running")
	}
	for _, want := range []string{
		"data-confighub-postgres-0 is Pending, and its events say it won't bind",
		"names no StorageClass",
		"Normal FailedBinding (x12): no persistent volumes available for this claim and no storage class is set",
		"kubectl --context eks -n confighub describe pvc data-confighub-postgres-0",
		"kubectl --context eks -n confighub delete statefulset confighub-postgres",
		"cub server install --kube-context eks --storage-class <name>",
		"--database=external --database-url <url>",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
}

// A StorageClass that doesn't exist won't bind either.
func TestAwaitDatabaseStopsOnAMissingStorageClass(t *testing.T) {
	stubWait(t, nil, time.Hour, func(int) *claimStatus {
		return &claimStatus{Phase: "Pending", StorageClass: "auto-gp3", Events: []claimEvent{{
			Type: "Warning", Reason: "ProvisioningFailed", Message: `storageclass.storage.k8s.io "auto-gp3" not found`,
		}}}
	})
	err := awaitDatabase(context.Background(), claimTestOptions(), kubeEnv{context: "eks"})
	if err == nil || !strings.Contains(err.Error(), "asks for StorageClass auto-gp3") ||
		!strings.Contains(err.Error(), `"auto-gp3" not found`) {
		t.Errorf("got %v", err)
	}
}

// Waiting for the pod to be scheduled, or for a slow provisioner, is not
// stuck. If the wait times out with the claim still Pending anyway, the claim
// is what is reported.
func TestAwaitDatabaseReportsAClaimStillPendingAtTheTimeout(t *testing.T) {
	cancelled := stubWait(t, errors.New("timed out waiting for the condition"), 20*time.Millisecond, func(int) *claimStatus {
		return &claimStatus{Phase: "Pending", StorageClass: "gp3", Events: []claimEvent{
			{Type: "Normal", Reason: "WaitForFirstConsumer", Message: "waiting for first consumer to be created before binding"},
			{Type: "Warning", Reason: "ProvisioningFailed", Message: "failed to provision volume: UnauthorizedOperation"},
		}}
	})
	err := awaitDatabase(context.Background(), claimTestOptions(), kubeEnv{context: "eks"})
	if cancelled.Load() {
		t.Error("a claim that may yet bind ended the wait early")
	}
	if err == nil || !strings.Contains(err.Error(), "still Pending after") || !strings.Contains(err.Error(), "UnauthorizedOperation") {
		t.Errorf("got %v", err)
	}
}

// A bound claim, or a database that comes up, says nothing of the claim.
func TestAwaitDatabaseWithABoundClaim(t *testing.T) {
	stubWait(t, nil, 10*time.Millisecond, func(int) *claimStatus { return &claimStatus{Phase: "Bound"} })
	if err := awaitDatabase(context.Background(), claimTestOptions(), kubeEnv{}); err != nil {
		t.Errorf("a database that came up: %v", err)
	}

	stubWait(t, errors.New("timed out waiting for the condition"), 10*time.Millisecond, func(int) *claimStatus {
		return &claimStatus{Phase: "Bound"}
	})
	err := awaitDatabase(context.Background(), claimTestOptions(), kubeEnv{})
	if err == nil || strings.Contains(err.Error(), "volume claim") {
		t.Errorf("a timeout with the claim bound is the rollout's to report: %v", err)
	}
}
