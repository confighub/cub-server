package install

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

// kubeEnv is the environment a kubectl invocation runs in.
//
// KUBECONFIG is set explicitly rather than inherited, so that an install into a
// cluster this tool created cannot be misdirected by whatever the shell's
// current context happens to be. That is the failure that installs a product
// into production because a terminal was left pointing there.
type kubeEnv struct {
	kubeconfig string // empty means the caller's default
	context    string // empty means the file's current-context
}

func (k kubeEnv) args(extra ...string) []string {
	var args []string
	if k.kubeconfig != "" {
		args = append(args, "--kubeconfig", k.kubeconfig)
	}
	if k.context != "" {
		args = append(args, "--context", k.context)
	}
	return append(args, extra...)
}

// apply applies one rendered manifest.
func (k kubeEnv) apply(ctx context.Context, content []byte) error {
	_, err := runIn(ctx, content, toolKubectl.name, k.args("apply", "-f", "-")...)
	return err
}

// applyFiles applies manifests in the order given. Order matters: the namespace
// has to exist before anything is created inside it, and the Secret before the
// Deployment that reads it.
func (k kubeEnv) applyFiles(ctx context.Context, paths []string) error {
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := k.apply(ctx, content); err != nil {
			return err
		}
	}
	return nil
}

// waitForRollout blocks until the deployment reports available.
//
// kubectl's own wait is used rather than polling, because it already knows what
// "available" means for each workload kind and reports the reason when it is
// not.
func (k kubeEnv) waitForRollout(ctx context.Context, namespace, kind, name string, timeout time.Duration) error {
	_, err := run(ctx, toolKubectl.name, k.args(
		"-n", namespace, "rollout", "status", kind+"/"+name,
		"--timeout", timeout.String(),
	)...)
	return err
}

// waitForAPI polls until the instance answers.
//
// A rollout reporting available means the pod's probes pass, which is not quite
// the same as the API being ready to serve this installer's next call -- and the
// next call is a login, whose failure would be reported as an auth problem
// rather than as a timing one.
//
// /api/info rather than a bare health endpoint: it is unauthenticated, and it is
// the endpoint that reports which authentication mechanisms this instance
// offers. Waiting on it therefore confirms the exact thing the next step
// depends on, rather than something correlated with it.
func waitForAPI(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}
	url := baseURL + "/api/info"

	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("%s returned %s", url, resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return fmt.Errorf("the server did not become ready at %s within %s: %w", baseURL, timeout, lastErr)
}
