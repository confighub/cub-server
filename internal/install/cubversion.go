package install

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Checking the cub that signs in.
//
// The cub found on PATH is not necessarily the one that ran this plugin: cub
// execs a plugin without saying where it lives, and an older cub earlier on
// PATH is easy to have. Two things make an old one the wrong one to sign in
// with:
//
//   - Before v0.8.2, `cub auth login` does not record where the server's UI is,
//     so `cub auth browser-session` -- which the install tells the operator to
//     run next -- cannot open it.
//   - cub treats a client behind its server in MAJOR.MINOR as incompatible
//     before 1.0: `cub version` and `cub auth status` fail on it, telling the
//     operator to upgrade. The login itself does not check, and may well
//     succeed, but it leaves a context that cub's own prerequisite check
//     rejects.
//
// So the version is checked before signing in. A cub that is too old is
// treated as a cub that is missing: the install has already succeeded, and the
// caller prints the commands to run once cub is upgraded.

// minimumCubVersion is the first cub whose login records the UI's address, so
// that `cub auth browser-session` opens it.
var minimumCubVersion = [3]int{0, 8, 2}

// cubUpgrade is how to get a newer cub.
const cubUpgrade = "cub upgrade"

// cubVersionTimeout bounds asking cub its version, since the fallback below can
// wait on a server.
const cubVersionTimeout = 10 * time.Second

// cubClientVersion asks cub for its own version.
//
// `cub version --client` prints the client alone, without contacting a server,
// but only from v0.8.31; an older cub rejects the flag. Plain `cub version`
// prints the client version first and then asks the current context's server
// for /api/info -- which may be a remote instance the operator uses for
// something else, and may not answer -- failing when it cannot. Either way the
// client's version is in the output, so it is read from there and the exit
// status is ignored.
var cubClientVersion = func(ctx context.Context, cub string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cubVersionTimeout)
	defer cancel()
	for _, args := range [][]string{{"version", "--client"}, {"version"}} {
		out, _ := runCub(ctx, cub, args...)
		if v, ok := clientVersionIn(out); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s version did not report a client version", cub)
}

// clientVersionIn reads the Version line of the "Client Version:" block of
// `cub version` output.
func clientVersionIn(out string) (string, bool) {
	inClient := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Client Version:":
			inClient = true
		case strings.HasSuffix(trimmed, "Version:") && !strings.HasPrefix(line, " "):
			inClient = false
		case inClient:
			if v, ok := strings.CutPrefix(trimmed, "Version:"); ok {
				v = strings.TrimSpace(v)
				return v, v != ""
			}
		}
	}
	return "", false
}

// parseCubVersion reads vMAJOR.MINOR[.PATCH] from a cub version, ignoring any
// pre-release or build suffix, as cub's own version check does. hasPatch is
// false for a version such as "v0.10-dev", which names a line rather than a
// release.
func parseCubVersion(s string) (v [3]int, hasPatch, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return v, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false, false
		}
		v[i] = n
	}
	return v, len(parts) == 3, true
}

// requiredCubVersion is the oldest cub to sign in to the server in image with:
// minimumCubVersion, or the server's own MAJOR.MINOR when that is newer. An
// image that is not a release adds nothing.
//
// The MAJOR.MINOR rule is cub's, from checkVersionSkew in cub's
// version_check.go, restated here because this has to decide before running
// any command that would apply it. If cub's rule changes, change this with it.
func requiredCubVersion(image string) [3]int {
	required := minimumCubVersion
	if server, _, ok := serverVersion(image); ok {
		line := [3]int{server[0], server[1], 0}
		if olderThan(required, line) {
			required = line
		}
	}
	return required
}

// cubTooOld reports whether a cub of version reported is older than required,
// and whether that could be told at all: a version that cannot be read -- a
// local build -- is unknown. A development line is compared by MAJOR.MINOR.
func cubTooOld(reported string, required [3]int) (old, known bool) {
	v, hasPatch, ok := parseCubVersion(reported)
	if !ok {
		return false, false
	}
	if !hasPatch {
		return olderThan([3]int{v[0], v[1], 0}, [3]int{required[0], required[1], 0}), true
	}
	return olderThan(v, required), true
}

// cubCheck is what checkCubVersion found out about a cub.
type cubCheck struct {
	// known is false when the version could not be read or understood.
	known bool
	// version is what cub reported, when it reported anything.
	version string
	// tooOld, when known, names the cub, its version and the fix.
	tooOld error
}

// checkCubVersion checks the cub at path against the server in image.
//
// Not being able to tell is its own answer, not "too old" and not "fine": the
// caller lets the login go ahead, since that will say what is wrong more
// precisely than a guess here, but says that the version is unknown.
func checkCubVersion(ctx context.Context, cub, image string) cubCheck {
	reported, err := cubClientVersion(ctx, cub)
	if err != nil {
		return cubCheck{}
	}
	required := requiredCubVersion(image)
	old, known := cubTooOld(reported, required)
	c := cubCheck{known: known, version: reported}
	if old {
		c.tooOld = fmt.Errorf("%s is cub %s; signing in to this instance needs v%d.%d.%d or later.\n"+
			"    This is the first cub on your PATH, which may not be the one that ran cub server.\n"+
			"    Upgrade it with: %s",
			cub, reported, required[0], required[1], required[2], cubUpgrade)
	}
	return c
}

// reportCubVersion says what checkCubVersion found, and reports whether cub can
// be used to sign in.
func reportCubVersion(u UI, cub string, c cubCheck) bool {
	switch {
	case c.tooOld != nil:
		u.warn("%v", c.tooOld)
		return false
	case !c.known && c.version != "":
		u.detail("cub: %s reports version %q, which is not a release; taking it to be current", cub, c.version)
	case !c.known:
		u.detail("cub: could not read the version of %s; signing in with it anyway", cub)
	default:
		u.detail("cub: %s (%s)", c.version, cub)
	}
	return true
}

// warnIfCubTooOld checks cub as soon as the server's version is known, which
// is before the cluster is built, so that an operator told to upgrade cub can
// do it while the install runs. The sign-in checks again, and picks up the
// upgrade.
func warnIfCubTooOld(ctx context.Context, u UI, o *Options) {
	if o.SkipAuth || o.DryRun {
		return
	}
	cub, err := cubBinary()
	if err != nil {
		return
	}
	if c := checkCubVersion(ctx, cub, o.Image); c.tooOld != nil {
		u.warn("%v", c.tooOld)
		u.warn("the install will go ahead, and leave signing in to you unless cub is upgraded before it finishes")
	}
}
