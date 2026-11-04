package install

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// Reading an instance's previous render.
//
// The render is found by what it holds, not by its file names, because there are
// two renderers: this plugin's numbered manifests (40-deployment.yaml) and the
// installer's, named after what is in them (deployment-confighub-confighub.yaml).
// A Deployment is the same Deployment in either, so asking for it by kind and
// name works for both.

// renderedObject returns the first document in the render's config directory
// with this kind and name, or nil when there is none: a first install has no
// render to read.
func renderedObject(outDir, kind, name string) ([]byte, error) {
	paths, err := filepath.Glob(filepath.Join(outDir, config.ConfigDir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if doc := objectIn(content, kind, name); doc != nil {
			return doc, nil
		}
	}
	return nil, nil
}

// objectIn returns the document in content with this kind and name, or nil.
func objectIn(content []byte, kind, name string) []byte {
	for _, text := range documentSeparator.Split(string(content), -1) {
		var meta struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if yaml.Unmarshal([]byte(text), &meta) != nil {
			continue
		}
		if meta.Kind == kind && meta.Metadata.Name == name {
			return []byte(text)
		}
	}
	return nil
}

// renderedConfigMapData returns a rendered ConfigMap's data, or nil when the
// render has no such ConfigMap.
func renderedConfigMapData(outDir, name string) (map[string]string, error) {
	doc, err := renderedObject(outDir, "ConfigMap", name)
	if doc == nil || err != nil {
		return nil, err
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(doc, &cm); err != nil {
		return nil, err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	return cm.Data, nil
}

// renderedContainerImage returns the image of a container of a rendered
// Deployment: the one named container, or the first when none is.
func renderedContainerImage(outDir, deployment, container string) (string, error) {
	doc, err := renderedObject(outDir, "Deployment", deployment)
	if doc == nil || err != nil {
		return "", err
	}
	var dep struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name  string `yaml:"name"`
						Image string `yaml:"image"`
					} `yaml:"containers"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(doc, &dep); err != nil {
		return "", err
	}
	containers := dep.Spec.Template.Spec.Containers
	for _, c := range containers {
		if c.Name == container {
			return c.Image, nil
		}
	}
	if len(containers) > 0 {
		return containers[0].Image, nil
	}
	return "", nil
}

// renderedNodePort returns a rendered Service's first node port, or zero.
func renderedNodePort(outDir, service string) int {
	doc, err := renderedObject(outDir, "Service", service)
	if doc == nil || err != nil {
		return 0
	}
	var svc struct {
		Spec struct {
			Ports []struct {
				NodePort int `yaml:"nodePort"`
			} `yaml:"ports"`
		} `yaml:"spec"`
	}
	if yaml.Unmarshal(doc, &svc) != nil || len(svc.Spec.Ports) == 0 {
		return 0
	}
	return svc.Spec.Ports[0].NodePort
}

// databaseStatefulSet is the bundled Postgres, as both renderers name it.
const databaseStatefulSet = "confighub-postgres"

// claimTemplate is the part of a StatefulSet's volume claim template an
// install chooses.
type claimTemplate struct {
	Spec struct {
		VolumeClaimTemplates []struct {
			Spec struct {
				StorageClassName *string `yaml:"storageClassName" json:"storageClassName"`
				Resources        struct {
					Requests struct {
						Storage string `yaml:"storage" json:"storage"`
					} `yaml:"requests" json:"requests"`
				} `yaml:"resources" json:"resources"`
			} `yaml:"spec" json:"spec"`
		} `yaml:"volumeClaimTemplates" json:"volumeClaimTemplates"`
	} `yaml:"spec" json:"spec"`
}

// classAndSize reads the first claim template's StorageClass, "" when it names
// none, and requested size.
func (c claimTemplate) classAndSize() (string, string) {
	if len(c.Spec.VolumeClaimTemplates) == 0 {
		return "", ""
	}
	spec := c.Spec.VolumeClaimTemplates[0].Spec
	class := ""
	if spec.StorageClassName != nil {
		class = *spec.StorageClassName
	}
	return class, spec.Resources.Requests.Storage
}

// renderedDatabaseClaim returns the StorageClass and size the rendered bundled
// database's volume claim asks for, or empty strings when the render has no
// bundled database or its claim names no class.
func renderedDatabaseClaim(outDir string) (class, size string, err error) {
	doc, err := renderedObject(outDir, "StatefulSet", databaseStatefulSet)
	if doc == nil || err != nil {
		return "", "", err
	}
	var sts claimTemplate
	if err := yaml.Unmarshal(doc, &sts); err != nil {
		return "", "", err
	}
	class, size = sts.classAndSize()
	return class, size, nil
}

// hasKeycloak reports whether the render includes the bundled identity
// provider, which is how any command can tell that an instance has one.
func hasKeycloak(outDir string) bool {
	doc, _ := renderedObject(outDir, "StatefulSet", config.KeycloakServiceName)
	return doc != nil
}
