package install

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Shelling out to kubectl rather than importing client-go.
//
// The cluster is created through kind's Go API (see kind.go), so Docker and
// kubectl are the only things that have to be installed. Replacing kubectl too
// would leave Docker alone, and is the same trade at a larger size: applying
// manifests and waiting on rollouts both have to be reimplemented, and kubectl
// already knows what "available" means for each workload kind.
//
// The boundary is kept narrow -- every invocation goes through here -- so making
// that change later is a change to this file rather than a change everywhere.

// tool is an external command this installer depends on.
type tool struct {
	name string
	// install is what to tell someone who does not have it.
	install string
}

var (
	toolKubectl = tool{name: "kubectl", install: "https://kubernetes.io/docs/tasks/tools/"}
	toolDocker  = tool{name: "docker", install: "https://docs.docker.com/get-started/get-docker/"}
)

// require reports a missing prerequisite as something actionable rather than as
// "executable file not found in $PATH".
func (t tool) require() error {
	if _, err := exec.LookPath(t.name); err != nil {
		return fmt.Errorf("%s is not on your PATH.\n    Install it from %s", t.name, t.install)
	}
	return nil
}

// run executes a command, returning its combined output. The output is folded
// into the error because these tools put the useful half on stderr and an error
// that says only "exit status 1" sends the reader to a terminal they no longer
// have.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

// runIn is run with stdin supplied, for `kubectl apply -f -`.
func runIn(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}
