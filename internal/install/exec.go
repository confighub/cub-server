package install

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// cubEnv is the environment a `cub` invocation runs in.
//
// cub tells a plugin which context, server, token and space the *caller* was
// using, by putting them in the plugin's environment. Those describe the
// instance the operator was already talking to, which is not the one being
// installed, and CUB_CONTEXT in particular is an override: a `cub` run with it
// set ignores the current context entirely.
//
// So a login that correctly created a new context was followed by a call that
// went to the old one, against a server that could not verify its token. Left
// in, they make every cub command here address the wrong instance.
//
// CUB_CONFIG is deliberately kept. It names the configuration store rather than
// a position within it, and the child has to read and write the same one.
func cubEnv() []string {
	drop := map[string]bool{
		"CUB_CONTEXT": true,
		"CUB_TOKEN":   true,
		"CUB_SERVER":  true,
		"CUB_SPACE":   true,
	}
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && drop[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// cubCommand is a cub command, with the caller's own context stripped out.
func cubCommand(ctx context.Context, cub string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, cub, args...)
	cmd.Env = cubEnv()
	return cmd
}

// runCub executes a cub command, returning its combined output.
func runCub(ctx context.Context, cub string, args ...string) (string, error) {
	cmd := cubCommand(ctx, cub, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w\n%s", cub, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

// runCubIn is runCub with stdin supplied, for a command whose stdout is data,
// such as `cub function local -`. Only stdout is returned, since cub logs to
// stderr; stderr is folded into the error instead.
//
// The error names the command by its arguments up to "--", and not the ones
// after it, which are a function's and carry values.
func runCubIn(ctx context.Context, stdin []byte, cub string, args ...string) ([]byte, error) {
	cmd := cubCommand(ctx, cub, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		named, _, _ := cutArgs(args, "--")
		return nil, fmt.Errorf("%s %s: %w\n%s", cub, strings.Join(named, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// cutArgs splits args around the first sep, as strings.Cut splits a string.
func cutArgs(args []string, sep string) (before, after []string, found bool) {
	for i, a := range args {
		if a == sep {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
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
