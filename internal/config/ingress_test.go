package config

import (
	"strings"
	"testing"
)

// The server serves /internal (pprof, metrics, an unauthenticated shutdown) on
// the API's port, so the ingress is what keeps it inside the cluster.
func TestIngressDoesNotRouteInternal(t *testing.T) {
	files := renderFiles(t, Options{
		Namespace: "confighub", Image: "img", UIImage: "ghcr.io/confighub/ui:v0.6.5",
		Ingress: IngressTraefik, Host: "hub.example.com",
	})
	ing, ok := files[ConfigDir+"/60-ingress.yaml"]
	if !ok {
		t.Fatal("no ingress rendered")
	}
	want := "match: Host(`hub.example.com`) && !PathPrefix(`/internal`)"
	if !strings.Contains(ing, want) {
		t.Errorf("the HTTPS route does not exclude /internal; want %q in:\n%s", want, ing)
	}
	if strings.Contains(ing, "confighub.example.com") {
		t.Errorf("the template's placeholder host survived rendering:\n%s", ing)
	}
}
