package config

import (
	"strings"
	"testing"
)

// With Metrics, the server's pod carries Prometheus's scrape annotations,
// naming the API's port and the path the server serves metrics on. Without,
// it carries none.
func TestMetricsAnnotations(t *testing.T) {
	base := Options{Namespace: "confighub", Image: "img", UIImage: "ghcr.io/confighub/ui:v0.6.5"}

	on := base
	on.Metrics = true
	dep := renderFiles(t, on)[ConfigDir+"/40-deployment.yaml"]
	for _, want := range []string{
		`prometheus.io/scrape: "true"`,
		`prometheus.io/port: "9090"`,
		`prometheus.io/path: /internal/metrics`,
		"confighub.com/config-hash:",
	} {
		if !strings.Contains(dep, want) {
			t.Errorf("no %s in the Deployment:\n%s", want, dep)
		}
	}

	dep = renderFiles(t, base)[ConfigDir+"/40-deployment.yaml"]
	if strings.Contains(dep, "prometheus.io/") {
		t.Errorf("annotated for Prometheus without Metrics:\n%s", dep)
	}
}
