package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Clusters that enforce the Pod Security Standards' restricted level refuse
// pods that do not meet it, and they refuse them at pod creation: the
// Deployment is accepted and its ReplicaSet never gets a pod. Every workload an
// install renders has to meet it.
func TestWorkloadsMeetRestrictedPodSecurity(t *testing.T) {
	files := renderFiles(t, Options{
		Namespace: "confighub", Image: "img:v1", UIImage: "ui:v1",
		APIURL: "http://localhost:32180", UIURL: "http://localhost:32183",
	})
	checked := 0
	for path, content := range files {
		dec := yaml.NewDecoder(strings.NewReader(content))
		for {
			var doc workload
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Kind != "Deployment" && doc.Kind != "StatefulSet" {
				continue
			}
			checked++
			name := path + " " + doc.Kind + "/" + doc.Metadata.Name
			pod := doc.Spec.Template.Spec
			if pod.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
				t.Errorf("%s: pod seccompProfile is %q, want RuntimeDefault", name, pod.SecurityContext.SeccompProfile.Type)
			}
			for _, c := range append(pod.InitContainers, pod.Containers...) {
				if !pod.SecurityContext.RunAsNonRoot && !c.SecurityContext.RunAsNonRoot {
					t.Errorf("%s: container %s may run as root", name, c.Name)
				}
				if c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
					t.Errorf("%s: container %s does not set allowPrivilegeEscalation: false", name, c.Name)
				}
				if len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
					t.Errorf("%s: container %s drops %v, want [ALL]", name, c.Name, c.SecurityContext.Capabilities.Drop)
				}
			}
		}
	}
	if checked < 3 {
		t.Fatalf("checked %d workloads; expected the server, the UI and the database", checked)
	}
}

type workload struct {
	Kind     string
	Metadata struct{ Name string }
	Spec     struct {
		Template struct {
			Spec struct {
				SecurityContext struct {
					RunAsNonRoot   bool `yaml:"runAsNonRoot"`
					SeccompProfile struct {
						Type string
					} `yaml:"seccompProfile"`
				} `yaml:"securityContext"`
				InitContainers []container `yaml:"initContainers"`
				Containers     []container
			}
		}
	}
}

type container struct {
	Name            string
	SecurityContext struct {
		RunAsNonRoot             bool  `yaml:"runAsNonRoot"`
		AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
		Capabilities             struct {
			Drop []string
		}
	} `yaml:"securityContext"`
}
