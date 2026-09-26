package install

import (
	"io"
	"strings"
	"testing"
)

// --- choosing the version to install ----------------------------------------

// The repository carries far more than releases, and only a release is a
// version anyone should be installed onto.
func TestHighestSemverIgnoresEverythingThatIsNotARelease(t *testing.T) {
	tags := []string{
		"latest", "buildcache-arm64", "buildcache-amd64",
		"amd64-v0.9.9", "arm64-v0.9.9", // per-arch tags, and higher than any release
		"testbuild", "v0.4.19", "v0.4.20",
	}
	got, err := highestSemver(tags)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.4.20" {
		t.Errorf("chose %q; a non-release tag was treated as a version", got)
	}
}

// The versions in play are exactly the range where string ordering is wrong:
// "v0.4.9" sorts after "v0.4.21" lexically.
func TestHighestSemverComparesNumbersNotStrings(t *testing.T) {
	got, err := highestSemver([]string{"v0.4.9", "v0.4.21", "v0.4.10"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.4.21" {
		t.Errorf("chose %q, want v0.4.21 -- compared lexically rather than numerically", got)
	}
}

func TestHighestSemverAcrossMajorAndMinor(t *testing.T) {
	got, err := highestSemver([]string{"v0.4.21", "v0.10.0", "v1.0.0", "v0.9.99"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.0.0" {
		t.Errorf("chose %q, want v1.0.0", got)
	}
}

func TestHighestSemverWithNoReleases(t *testing.T) {
	if _, err := highestSemver([]string{"latest", "buildcache-amd64"}); err == nil {
		t.Fatal("expected an error when nothing in the repository is a release")
	}
}

// --image is a deliberate choice and is never second-guessed.
func TestResolveImageKeepsAnExplicitChoice(t *testing.T) {
	o := &Options{Image: "mirror.internal/confighub:v0.7.0"}
	if err := resolveImage(discardUI(), o, "ghcr.io/confighubai/confighub:v0.8.0"); err != nil {
		t.Fatal(err)
	}
	if o.Image != "mirror.internal/confighub:v0.7.0" {
		t.Errorf("--image was overridden, got %q", o.Image)
	}
}

// Re-running an install resumes it. Moving the server to whatever is newest
// because someone re-applied their configuration would be a deploy they did not
// ask for.
func TestResolveImageDoesNotUpgradeOnAReRun(t *testing.T) {
	o := &Options{}
	if err := resolveImage(discardUI(), o, "ghcr.io/confighubai/confighub:v0.6.5"); err != nil {
		t.Fatal(err)
	}
	if o.Image != "ghcr.io/confighubai/confighub:v0.6.5" {
		t.Errorf("a re-run changed the image to %q", o.Image)
	}
}

// With no network and no --image there is nothing to install, and saying so is
// better than installing a version nobody chose.
func TestResolveImageFailsRatherThanGuessing(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")

	o := &Options{}
	err := resolveImage(discardUI(), o, "")
	if err == nil {
		t.Fatal("expected the install to stop; there is no version to install")
	}
	if !strings.Contains(err.Error(), "--image") {
		t.Errorf("the error should name the way out, got: %v", err)
	}
	if o.Image != "" {
		t.Errorf("no image should have been chosen, got %q", o.Image)
	}
}

func discardUI() UI { return UI{Out: io.Discard} }

// A released server older than the first one this plugin configures is refused,
// whichever way it was chosen. The rendered configuration would stop it
// starting, and it has no UI release to go with it.
func TestResolveImageRefusesAServerTooOld(t *testing.T) {
	for _, tc := range []struct{ explicit, prior string }{
		{explicit: "ghcr.io/confighubai/confighub:v0.6.4"},
		{prior: "ghcr.io/confighubai/confighub:v0.4.2"},
	} {
		o := &Options{Image: tc.explicit}
		err := resolveImage(discardUI(), o, tc.prior)
		if err == nil || !strings.Contains(err.Error(), "v0.6.5") {
			t.Errorf("%+v: want a refusal naming the minimum version, got %v", tc, err)
		}
	}
}

// A server image that is not a release is the caller's to vouch for.
func TestResolveImageAcceptsAnUnreleasedServer(t *testing.T) {
	o := &Options{Image: "confighub:dev"}
	if err := resolveImage(discardUI(), o, ""); err != nil {
		t.Fatalf("a local build was refused: %v", err)
	}
}

// The UI is released with the server under the same version, without the "v".
func TestUIImageFollowsTheServerVersion(t *testing.T) {
	o := &Options{Image: "ghcr.io/confighubai/confighub:v0.6.5", OutDir: t.TempDir()}
	if err := resolveUIImage(discardUI(), o); err != nil {
		t.Fatal(err)
	}
	if want := "ghcr.io/confighub/ui:0.6.5"; o.UIImage != want {
		t.Errorf("UI image = %q, want %q", o.UIImage, want)
	}

	o = &Options{Image: "ghcr.io/confighubai/confighub:v0.6.5", UIImage: "mine/ui:x"}
	if err := resolveUIImage(discardUI(), o); err != nil || o.UIImage != "mine/ui:x" {
		t.Errorf("--ui-image was overridden: %q, %v", o.UIImage, err)
	}

	// A local server build has no UI release to match, so one must be named.
	o = &Options{Image: "confighub:dev", OutDir: t.TempDir()}
	if err := resolveUIImage(discardUI(), o); err == nil || !strings.Contains(err.Error(), "--ui-image") {
		t.Errorf("want a request for --ui-image, got %v", err)
	}
}
