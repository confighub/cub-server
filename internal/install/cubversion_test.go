package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCub writes a shell script standing in for cub, and returns its path.
func fakeCub(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A current cub answers --client with the client alone.
func TestCubClientVersionFromClientFlag(t *testing.T) {
	cub := fakeCub(t, `[ "$2" = "--client" ] || exit 2
printf 'Client Version:\n  Version:    v0.10.9\n  Commit:     abc\n'
`)
	v, err := cubClientVersion(context.Background(), cub)
	if err != nil || v != "v0.10.9" {
		t.Fatalf("got %q, %v; want v0.10.9", v, err)
	}
}

// A cub before v0.8.31 rejects --client, and its plain `cub version` fails once
// it cannot reach a server. The client version it printed first is still read.
func TestCubClientVersionFromAnOldCub(t *testing.T) {
	cub := fakeCub(t, `if [ "$2" = "--client" ]; then echo 'Failed: unknown flag: --client'; exit 1; fi
printf 'Client Version:\n  Version:    v0.6.2\n  Commit:     abc\nServer Version:\n  Version:    \nFailed: connection refused\n'
exit 1
`)
	v, err := cubClientVersion(context.Background(), cub)
	if err != nil || v != "v0.6.2" {
		t.Fatalf("got %q, %v; want v0.6.2", v, err)
	}
}

// The server's Version line must not be taken for the client's.
func TestClientVersionInIgnoresTheServerBlock(t *testing.T) {
	out := "Client Version:\n  Commit: abc\nServer Version:\n  Version:    v0.10.9\n"
	if v, ok := clientVersionIn(out); ok {
		t.Errorf("read %q from the server block", v)
	}
}

func TestRequiredCubVersion(t *testing.T) {
	for _, c := range []struct {
		image string
		want  [3]int
	}{
		// cub refuses a server ahead of it in the second number.
		{"ghcr.io/confighubai/confighub:v0.10.9", [3]int{0, 10, 0}},
		// The UI address is recorded from v0.8.2, whatever the server.
		{"ghcr.io/confighubai/confighub:v0.8.0", [3]int{0, 8, 2}},
		// Not a release: only the floor.
		{"localhost:5000/confighub:dev", minimumCubVersion},
		{"", minimumCubVersion},
	} {
		if got := requiredCubVersion(c.image); got != c.want {
			t.Errorf("requiredCubVersion(%q) = %v, want %v", c.image, got, c.want)
		}
	}
}

func TestCubTooOld(t *testing.T) {
	required := [3]int{0, 10, 0}
	for _, c := range []struct {
		version    string
		old, known bool
	}{
		{"v0.6.2", true, true},
		{"v0.9.40", true, true},
		{"v0.10.0", false, true},
		{"v0.10.9", false, true},
		{"v0.11.1", false, true},
		{"0.10.3", false, true},
		{"v0.10.3-rc1", false, true},
		// A development line is compared by MAJOR.MINOR.
		{"v0.10-dev", false, true},
		{"v0.9-dev", true, true},
		// Unreadable: a local build. Unknown, not "current".
		{"dev", false, false},
		{"", false, false},
	} {
		old, known := cubTooOld(c.version, required)
		if old != c.old || known != c.known {
			t.Errorf("cubTooOld(%q) = %v, %v; want %v, %v", c.version, old, known, c.old, c.known)
		}
	}
	if old, _ := cubTooOld("v0.8.1", minimumCubVersion); !old {
		t.Error("v0.8.1 predates the login recording the UI's address")
	}
	if old, _ := cubTooOld("v0.8.2", minimumCubVersion); old {
		t.Error("v0.8.2 is the first cub whose login records the UI's address")
	}
}

const testImage = "ghcr.io/confighubai/confighub:v0.10.9"

// The error names the binary, so an operator with two cubs knows which one to
// upgrade, and the command that upgrades it.
func TestCheckCubVersionNamesTheBinaryAndTheFix(t *testing.T) {
	cub := fakeCub(t, `printf 'Client Version:\n  Version:    v0.6.2\n'
`)
	c := checkCubVersion(context.Background(), cub, testImage)
	if c.tooOld == nil {
		t.Fatal("cub v0.6.2 should be too old for server v0.10.9")
	}
	for _, want := range []string{cub, "v0.6.2", "v0.10.0", cubUpgrade} {
		if !strings.Contains(c.tooOld.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, c.tooOld)
		}
	}

	current := fakeCub(t, `printf 'Client Version:\n  Version:    v0.10.9\n'
`)
	if c := checkCubVersion(context.Background(), current, testImage); c.tooOld != nil || !c.known {
		t.Errorf("a matching cub was not accepted: %+v", c)
	}
}

// A cub whose version cannot be read is let through to the login, but as
// unknown, and the install says so rather than reporting it as current.
func TestUnreadableCubVersionIsReportedAsUnknown(t *testing.T) {
	cub := fakeCub(t, "echo nonsense\nexit 1\n")
	c := checkCubVersion(context.Background(), cub, testImage)
	if c.tooOld != nil || c.known {
		t.Fatalf("unreadable version should be unknown and not refused: %+v", c)
	}
	var out strings.Builder
	if !reportCubVersion(UI{Out: &out}, cub, c) {
		t.Error("an unknown version should not stop the sign-in")
	}
	if !strings.Contains(out.String(), "could not read the version") {
		t.Errorf("the unknown version should be reported, got: %q", out.String())
	}
}

// authenticateWith runs the sign-in step with script as the only cub on PATH,
// and returns what it printed and the cub commands it ran.
func authenticateWith(t *testing.T, script string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(filepath.Join(dir, "cub"),
		[]byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CUB_CONFIG", t.TempDir())
	o := &Options{
		APINodePort:  30080,
		UINodePort:   30081,
		Namespace:    "confighub",
		OutDir:       t.TempDir(),
		AdminKeyName: "admin",
		Image:        testImage,
	}
	var out strings.Builder
	err := authenticate(context.Background(), UI{Out: &out}, o, "/keys/admin")
	calls, _ := os.ReadFile(log)
	return out.String(), string(calls), err
}

// A cub too old for the instance does not sign in. The install still
// succeeds, and ends with the upgrade ahead of the commands that need it.
func TestAuthenticateWithAnOldCubPrintsTheUpgradeFirst(t *testing.T) {
	out, calls, err := authenticateWith(t, `printf 'Client Version:\n  Version:    v0.6.2\n'
`)
	if err != nil {
		t.Fatalf("an old cub should not fail the install: %v", err)
	}
	if strings.Contains(calls, "auth login") {
		t.Errorf("signed in with a cub too old for the instance; ran:\n%s", calls)
	}
	upgrade := strings.Index(out, "  "+cubUpgrade+"\n")
	login := strings.Index(out, "  cub auth login")
	if upgrade < 0 || login < 0 || upgrade > login {
		t.Errorf("want %q listed before the sign-in commands, got:\n%s", cubUpgrade, out)
	}
}

// A current cub signs in.
func TestAuthenticateWithACurrentCubSignsIn(t *testing.T) {
	out, calls, err := authenticateWith(t, `[ "$1" = version ] && printf 'Client Version:\n  Version:    v0.10.9\n'
exit 0
`)
	if err != nil {
		t.Fatalf("authenticate: %v\n%s", err, out)
	}
	if !strings.Contains(calls, "auth login") || !strings.Contains(calls, "space list") {
		t.Errorf("want a login and a verifying call; ran:\n%s", calls)
	}
	if strings.Contains(out, cubUpgrade) {
		t.Errorf("a current cub was told to upgrade:\n%s", out)
	}
}
