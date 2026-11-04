package install

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Saying why the database doesn't come up.
//
// Waiting on the database's rollout alone, a claim that can never bind is five
// minutes of nothing followed by kubectl's "timed out waiting for the
// condition": the pod is Pending because its claim is, and only the claim's
// events say why. So the wait watches the claim too, stops as soon as its
// events say it will never bind, and otherwise, if the wait times out with the
// claim still Pending, reports the claim rather than the timeout.

// databaseClaim is the PersistentVolumeClaim the StatefulSet makes for its one
// replica, named after its claim template, data.
const databaseClaim = "data-" + databaseStatefulSet + "-0"

// claimCheckInterval is how often the wait looks at the claim.
const claimCheckInterval = 5 * time.Second

// claimStatus is what the wait needs to know about a claim.
type claimStatus struct {
	Phase        string
	StorageClass string // "" when the claim names none
	Events       []claimEvent
}

// claimEvent is one of a claim's events, oldest first.
type claimEvent struct {
	Type, Reason, Message string
	Count                 int
}

// neverBinds reports whether the claim's events say it is waiting on something
// only the operator can change: no StorageClass and no volume to bind to, or a
// StorageClass that doesn't exist. Anything else may yet resolve itself -- a
// class whose binding waits for the pod to be scheduled, a provisioner that is
// slow or retrying -- and is waited out.
//
// By reason, not type: the volume controller reports FailedBinding as a Normal
// event, and a missing class's ProvisioningFailed as a Warning.
func (c *claimStatus) neverBinds() bool {
	if c == nil || c.Phase != "Pending" {
		return false
	}
	for _, e := range c.Events {
		if e.Reason == "FailedBinding" ||
			(e.Reason == "ProvisioningFailed" && strings.Contains(e.Message, "storageclass") && strings.Contains(e.Message, "not found")) {
			return true
		}
	}
	return false
}

// claim reads a claim and its events, or returns nil when there is no such claim
// yet.
func (k kubeEnv) claim(ctx context.Context, namespace, name string) (*claimStatus, error) {
	out, err := run(ctx, toolKubectl.name, k.args("-n", namespace, "get", "pvc", name, "-o", "json", "--ignore-not-found")...)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var pvc struct {
		Spec struct {
			StorageClassName *string `json:"storageClassName"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &pvc); err != nil {
		return nil, fmt.Errorf("reading pvc %s/%s: %w", namespace, name, err)
	}
	status := &claimStatus{Phase: pvc.Status.Phase}
	if pvc.Spec.StorageClassName != nil {
		status.StorageClass = *pvc.Spec.StorageClassName
	}

	out, err = run(ctx, toolKubectl.name, k.args("-n", namespace, "get", "events",
		"--field-selector", "involvedObject.kind=PersistentVolumeClaim,involvedObject.name="+name,
		"--sort-by", ".lastTimestamp", "-o", "json")...)
	if err != nil {
		// The claim's phase is still worth reporting without them.
		return status, nil
	}
	var events struct {
		Items []struct {
			Type    string `json:"type"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
			Count   int    `json:"count"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(out), &events) == nil {
		for _, e := range events.Items {
			status.Events = append(status.Events, claimEvent{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count})
		}
	}
	return status, nil
}

// awaitDatabase waits for the bundled database's rollout, looking at its claim
// every checkEvery.
func awaitDatabase(ctx context.Context, o *Options, cluster storageCluster, checkEvery time.Duration) error {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- cluster.waitForRollout(waitCtx, o.Namespace, "statefulset", databaseStatefulSet, rolloutTimeout)
	}()

	tick := time.NewTicker(checkEvery)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err == nil || ctx.Err() != nil {
				return err
			}
			if claim, _ := cluster.claim(ctx, o.Namespace, databaseClaim); claim != nil && claim.Phase == "Pending" {
				return pendingClaimError(o, cluster.commandFlags(), claim, "still Pending after "+rolloutTimeout.String())
			}
			return err
		case <-tick.C:
			claim, err := cluster.claim(ctx, o.Namespace, databaseClaim)
			if err == nil && claim.neverBinds() {
				cancel()
				<-done
				return pendingClaimError(o, cluster.commandFlags(), claim, "Pending, and its events say it won't bind")
			}
		}
	}
}

// pendingClaimError says why the database's claim is Pending, as far as the
// claim knows, and what to do about it. k is the kubectl flags that reach the
// cluster.
func pendingClaimError(o *Options, k string, claim *claimStatus, state string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "the database's volume claim %s is %s, so Postgres can't start.\n", databaseClaim, state)
	if claim.StorageClass == "" {
		b.WriteString("    It names no StorageClass, so it needs the cluster's default one.\n")
	} else {
		fmt.Fprintf(&b, "    It asks for StorageClass %s.\n", claim.StorageClass)
	}

	// The latest few: a claim retried for minutes has a long history saying the
	// same thing.
	events := claim.Events
	if len(events) > 5 {
		events = events[len(events)-5:]
	}
	if len(events) > 0 {
		b.WriteString("    Its events:\n")
		for _, e := range events {
			count := ""
			if e.Count > 1 {
				count = fmt.Sprintf(" (x%d)", e.Count)
			}
			fmt.Fprintf(&b, "      %s %s%s: %s\n", e.Type, e.Reason, count, e.Message)
		}
	}
	fmt.Fprintf(&b, "\n    kubectl%s -n %s describe pvc %s\n", k, o.Namespace, databaseClaim)

	// A StatefulSet's claim template can't change in place, so another class
	// means deleting both first. The claim never bound, so there is no data.
	install := "cub server install"
	if o.Target == TargetContext {
		install += " --kube-context " + o.KubeContext
	}
	install += outDirFlag(o)
	fmt.Fprintf(&b, "\n    Name a StorageClass the cluster has (kubectl%s get storageclass). The claim never\n", k)
	b.WriteString("    bound, so there is no data to lose; delete it and its StatefulSet, then re-install:\n")
	fmt.Fprintf(&b, "      kubectl%s -n %s delete statefulset %s\n", k, o.Namespace, databaseStatefulSet)
	fmt.Fprintf(&b, "      kubectl%s -n %s delete pvc %s\n", k, o.Namespace, databaseClaim)
	fmt.Fprintf(&b, "      %s --storage-class <name>\n", install)
	fmt.Fprintf(&b, "\n    Or, with both deleted, bring your own database and need no volume here:\n")
	fmt.Fprintf(&b, "      %s --database=external --database-url <url>", install)
	return fmt.Errorf("%s", b.String())
}
