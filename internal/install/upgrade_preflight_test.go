package install

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A network that cannot reach the first release registry still learns the
// newest version from the second.
func TestNewestReleaseFallsBackToTheNextRegistry(t *testing.T) {
	prev := listReleaseTags
	t.Cleanup(func() { listReleaseTags = prev })

	var asked []string
	listReleaseTags = func(_ context.Context, repo string) ([]string, error) {
		asked = append(asked, repo)
		if repo == GHCR.Server {
			return nil, errors.New("dial tcp: i/o timeout")
		}
		return []string{"v0.11.2", "v0.11.18", "latest"}, nil
	}
	got, err := newestRelease(context.Background())
	if err != nil || got != "v0.11.18" {
		t.Fatalf("newestRelease = %q, %v; want v0.11.18 from the second registry", got, err)
	}
	if len(asked) != 2 || asked[0] != GHCR.Server || asked[1] != GCR.Server {
		t.Errorf("asked %v, want ghcr then gcr", asked)
	}

	listReleaseTags = func(context.Context, string) ([]string, error) { return nil, errors.New("blocked") }
	_, err = newestRelease(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--version") ||
		!strings.Contains(err.Error(), GHCR.Server) || !strings.Contains(err.Error(), GCR.Server) {
		t.Errorf("with no registry reachable: %v, want both named and --version offered", err)
	}
}

// Only a registry that was asked and answered 404 says an image is missing. One
// that wants credentials, as a private mirror does, says nothing either way.
func TestCheckImage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "image.index") {
			t.Errorf("%s %s with Accept %q, want a HEAD that accepts an index", r.Method, r.URL.Path, r.Header.Get("Accept"))
		}
		switch r.URL.Path {
		case "/v2/mirror/confighub/manifests/v0.11.18":
			w.WriteHeader(http.StatusOK)
		case "/v2/private/confighub/manifests/v0.11.18":
			w.Header().Set("WWW-Authenticate", `Basic realm="Artifactory"`)
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")

	for _, tc := range []struct {
		image string
		want  imagePresence
	}{
		{host + "/mirror/confighub:v0.11.18", imageFound},
		{host + "/mirror/confighub:v0.99.0", imageMissing},
		{host + "/private/confighub:v0.11.18", imageUnknown},
		{"confighub:v0.11.18", imageUnknown},
		{"127.0.0.1:1/mirror/confighub:v0.11.18", imageUnknown},
	} {
		got := checkImage(context.Background(), srv.Client(), tc.image)
		if got.presence != tc.want {
			t.Errorf("checkImage(%s) = %d (%s), want %d", tc.image, got.presence, got.why, tc.want)
		}
		if got.presence == imageUnknown && got.why == "" {
			t.Errorf("checkImage(%s) is unknown without saying why", tc.image)
		}
	}
}

func TestImageCheckLines(t *testing.T) {
	lines, missing := imageCheckLines([]imageCheck{
		{image: "gcr.io/confighubapp/confighub:v0.11.18", presence: imageFound},
		{image: "mirror.example.com/ui:v0.11.18", presence: imageMissing},
		{image: "private.example.com/ui:v0.11.18", why: "the registry wants credentials"},
	})
	if len(missing) != 1 || missing[0] != "mirror.example.com/ui:v0.11.18" {
		t.Errorf("missing = %v, want only the image a registry said it lacks", missing)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"found      gcr.io/confighubapp/confighub:v0.11.18",
		"NOT FOUND  mirror.example.com/ui:v0.11.18",
		"unchecked  private.example.com/ui:v0.11.18  (the registry wants credentials)",
		"Copy the missing images",
		"stays down if the pull fails",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

// The plan lists each image it moves to once, though the server's image runs in
// two containers.
func TestPlanUpgradeListsItsImages(t *testing.T) {
	const mirror = "artifactory.acme.com/ghcr"
	server := &deploymentImages{
		name:           "confighub",
		containers:     map[string]string{"confighub": mirror + "/confighub:v0.10.8"},
		initContainers: map[string]string{"confighub-db-init-and-migrate": mirror + "/confighub:v0.10.8"},
	}
	ui := &deploymentImages{name: "confighub-ui", containers: map[string]string{"ui": mirror + "/ui:v0.10.8"}}
	plan := planUpgrade("confighub", server, ui, server.containers["confighub"], "v0.10.8", "v0.11.18")
	want := []string{mirror + "/confighub:v0.11.18", mirror + "/ui:v0.11.18"}
	if strings.Join(plan.images, " ") != strings.Join(want, " ") {
		t.Errorf("images = %v, want %v", plan.images, want)
	}
}

func TestBackupLines(t *testing.T) {
	kube := kubeEnv{context: "prod"}
	bundled := strings.Join(backupLines(kube, "confighub", "v0.10.8", true), "\n")
	if !strings.Contains(bundled, "--context prod") ||
		!strings.Contains(bundled, "-n confighub exec statefulset/confighub-postgres -- pg_dump -U confighub -Fc confighub > confighub-v0.10.8.dump") {
		t.Errorf("bundled database:\n%s", bundled)
	}
	external := strings.Join(backupLines(kube, "confighub", "v0.10.8", false), "\n")
	if strings.Contains(external, "pg_dump") || !strings.Contains(external, "DATABASE_URL") {
		t.Errorf("external database:\n%s", external)
	}
}

// Clients have to move only when the second version number does.
func TestMinorChangeLines(t *testing.T) {
	if lines := minorChangeLines([3]int{0, 11, 2}, [3]int{0, 11, 18}); lines != nil {
		t.Errorf("a patch upgrade printed %q", lines)
	}
	text := strings.Join(minorChangeLines([3]int{0, 10, 8}, [3]int{0, 11, 18}), "\n")
	for _, want := range []string{"from 0.10 to 0.11", "cub upgrade", "k8sbot or argobot", "your own clients"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

// The registry's address in parts is reported once the target reads
// OCI_EXTERNAL_URL, and only while the URL is not set.
func TestUnusedEnvRegistryAddressParts(t *testing.T) {
	parts := []envSource{{kind: "ConfigMap", name: "confighub-env",
		keys: []string{"OCI_EXTERNAL_HOST", "OCI_EXTERNAL_PORT", "OCI_TOKEN_REALM"}}}

	got := unusedEnv(parts, [3]int{0, 11, 18})
	if len(got) != 3 {
		t.Fatalf("unusedEnv = %+v, want the three parts", got)
	}
	for _, f := range got {
		if !strings.Contains(f.advice, "still read") || !strings.Contains(f.advice, "OCI_EXTERNAL_URL") {
			t.Errorf("%s: advice %q, want it to say the variable is still read and name OCI_EXTERNAL_URL", f.name, f.advice)
		}
	}
	if got := unusedEnv(parts, [3]int{0, 10, 3}); len(got) != 0 {
		t.Errorf("before v0.10.4: %+v, want nothing", got)
	}
	withURL := []envSource{{kind: "ConfigMap", name: "confighub-env", keys: []string{"OCI_EXTERNAL_URL", "OCI_EXTERNAL_PORT"}}}
	if got := unusedEnv(withURL, [3]int{0, 11, 18}); len(got) != 0 {
		t.Errorf("beside OCI_EXTERNAL_URL: %+v, want nothing", got)
	}
}

// A registry lists tags a page at a time. The newest release of a repository
// with more tags than fit on one page is on a later page.
func TestListTagsReadsEveryPage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/confighubai/confighub/tags/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Query().Get("last") {
		case "":
			w.Header().Set("Link", `</v2/confighubai/confighub/tags/list?last=v0.11.15&n=1000>; rel="next"`)
			w.Write([]byte(`{"tags":["v0.11.14","amd64-v0.11.15","v0.11.15"]}`))
		case "v0.11.15":
			w.Write([]byte(`{"tags":["v0.11.16","v0.11.18","v0.11.17"]}`))
		default:
			t.Errorf("asked for the page after %q", r.URL.Query().Get("last"))
		}
	}))
	defer srv.Close()
	prev := registryClient
	registryClient = srv.Client()
	t.Cleanup(func() { registryClient = prev })

	image, err := resolveLatestImage(context.Background(), strings.TrimPrefix(srv.URL, "https://")+"/confighubai/confighub")
	if err != nil || !strings.HasSuffix(image, ":v0.11.18") {
		t.Errorf("resolveLatestImage = %q, %v; want v0.11.18 from the second page", image, err)
	}
}
