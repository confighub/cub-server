package install

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestImageRepo(t *testing.T) {
	for image, want := range map[string]string{
		"ghcr.io/confighubai/confighub:v0.9.5":           "ghcr.io/confighubai/confighub",
		"registry.internal:5000/confighub:v0.9.5":        "registry.internal:5000/confighub",
		"registry.internal:5000/confighub":               "registry.internal:5000/confighub",
		"ghcr.io/confighub/ui@sha256:abc":                "ghcr.io/confighub/ui",
		"ghcr.io/confighub/ui:v0.9.5@sha256:abc":         "ghcr.io/confighub/ui",
		"artifactory.acme.com/ghcr/confighub/ui:v0.8.38": "artifactory.acme.com/ghcr/confighub/ui",
	} {
		if got := imageRepo(image); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", image, got, want)
		}
	}
}

// The registry is the operator's choice and is kept; only the tag moves. In the
// server's Deployment only containers running the server's image move, so a
// sidecar someone added is left alone, and the init container that migrates
// the database moves with the server.
func TestPlanUpgrade(t *testing.T) {
	const mirror = "artifactory.acme.com/ghcr"
	server := &deploymentImages{
		name: "confighub",
		containers: map[string]string{
			"confighub": mirror + "/confighubai/confighub:v0.8.38",
			"sidecar":   "envoyproxy/envoy:v1.30",
		},
		initContainers: map[string]string{"confighub-db-init-and-migrate": mirror + "/confighubai/confighub:v0.8.38"},
	}
	ui := &deploymentImages{name: "confighub-ui", containers: map[string]string{"ui": mirror + "/confighub/ui:v0.8.38"}}

	plan := planUpgrade("confighub", server, ui, server.containers["confighub"], "v0.8.38", "v0.9.5")
	if len(plan.patches) != 2 || plan.patches[0].deployment != "confighub" || plan.patches[1].deployment != "confighub-ui" {
		t.Fatalf("patches = %+v, want the server's then the UI's", plan.patches)
	}

	var patch struct {
		Kind     string
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			Template struct {
				Spec struct {
					Containers     []struct{ Name, Image string } `yaml:"containers"`
					InitContainers []struct{ Name, Image string } `yaml:"initContainers"`
				}
			}
		}
	}
	if err := yaml.Unmarshal(plan.patches[0].body, &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Kind != "Deployment" || patch.Metadata.Name != "confighub" || patch.Metadata.Namespace != "confighub" {
		t.Errorf("patch is for %s %s", patch.Kind, patch.Metadata.Name)
	}
	spec := patch.Spec.Template.Spec
	if len(spec.Containers) != 1 || spec.Containers[0].Name != "confighub" || spec.Containers[0].Image != mirror+"/confighubai/confighub:v0.9.5" {
		t.Errorf("containers = %+v, want only the server, on the mirror", spec.Containers)
	}
	if len(spec.InitContainers) != 1 || spec.InitContainers[0].Image != mirror+"/confighubai/confighub:v0.9.5" {
		t.Errorf("init containers = %+v", spec.InitContainers)
	}
	if !strings.Contains(string(plan.patches[1].body), mirror+"/confighub/ui:v0.9.5") {
		t.Errorf("UI patch = %s", plan.patches[1].body)
	}

	// Already there: nothing to patch.
	server.containers["confighub"] = mirror + "/confighubai/confighub:v0.9.5"
	server.initContainers["confighub-db-init-and-migrate"] = mirror + "/confighubai/confighub:v0.9.5"
	ui.containers["ui"] = mirror + "/confighub/ui:v0.9.5"
	if plan := planUpgrade("confighub", server, ui, server.containers["confighub"], "v0.9.5", "v0.9.5"); len(plan.patches) != 0 {
		t.Errorf("planned %d patches for an instance already on the target", len(plan.patches))
	}
}
