package install

import (
	"io"
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
	o := &Options{Image: "mirror.internal/confighub:v0.1.0"}
	if err := resolveImage(discardUI(), o, "ghcr.io/confighubai/confighub:v0.4.20"); err != nil {
		t.Fatal(err)
	}
	if o.Image != "mirror.internal/confighub:v0.1.0" {
		t.Errorf("--image was overridden, got %q", o.Image)
	}
}

// Re-running an install resumes it. Moving the server to whatever is newest
// because someone re-applied their configuration would be a deploy they did not
// ask for.
func TestResolveImageDoesNotUpgradeOnAReRun(t *testing.T) {
	o := &Options{}
	if err := resolveImage(discardUI(), o, "ghcr.io/confighubai/confighub:v0.4.2"); err != nil {
		t.Fatal(err)
	}
	if o.Image != "ghcr.io/confighubai/confighub:v0.4.2" {
		t.Errorf("a re-run changed the image to %q", o.Image)
	}
}

func discardUI() UI { return UI{Out: io.Discard} }
