package config

import (
	"embed"
	"fmt"
	"path"

	"github.com/confighub/sdk/core/third_party/gaby"
)

//go:embed manifests/*.yaml
var manifests embed.FS

// configHashAnnotation is the pod-template annotation carrying the hash of the
// configuration the Deployment reads. See the comment on it in the manifest.
const configHashAnnotation = "confighub.com/config-hash"

// load reads a manifest and parses its documents.
func load(name string) (gaby.Container, error) {
	content, err := manifests.ReadFile(path.Join("manifests", name))
	if err != nil {
		return nil, err
	}
	docs, err := gaby.ParseAll(content)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", name, err)
	}
	return docs, nil
}

// namespaceEdits are the edits every manifest gets. The namespace is on every
// object, and each document in a file carries its own.
func namespaceEdits(docs gaby.Container, namespace string) []edit {
	edits := make([]edit, 0, len(docs))
	for i := range docs {
		edits = append(edits, edit{doc: i, path: "metadata.namespace", value: namespace})
	}
	return edits
}

func renderNamespace(opts Options) ([]byte, error) {
	docs, err := load("00-namespace.yaml")
	if err != nil {
		return nil, err
	}
	if err := apply(docs, []edit{{path: "metadata.name", value: opts.Namespace}}); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderServiceAccount(opts Options) ([]byte, error) {
	docs, err := load("10-service-account.yaml")
	if err != nil {
		return nil, err
	}
	if err := apply(docs, namespaceEdits(docs, opts.Namespace)); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderConfigMap(opts Options, entries []entry) ([]byte, error) {
	docs, err := load("20-configmap.yaml")
	if err != nil {
		return nil, err
	}
	if err := apply(docs, namespaceEdits(docs, opts.Namespace)); err != nil {
		return nil, err
	}
	if err := fillMap(docs[0], "data", entries); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderSecret(opts Options, entries []entry) ([]byte, error) {
	docs, err := load("secret.yaml")
	if err != nil {
		return nil, err
	}
	if err := apply(docs, namespaceEdits(docs, opts.Namespace)); err != nil {
		return nil, err
	}
	if err := fillMap(docs[0], "stringData", entries); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderDatabase(opts Options) ([]byte, error) {
	docs, err := load("30-database.yaml")
	if err != nil {
		return nil, err
	}
	edits := namespaceEdits(docs, opts.Namespace)
	edits = append(edits,
		edit{doc: 0, path: "spec.template.spec.containers.0.image", value: opts.DatabaseImage},
		edit{doc: 0, path: "spec.volumeClaimTemplates.0.spec.resources.requests.storage", value: opts.StorageSize},
	)
	if err := apply(docs, edits); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderDeployment(opts Options, configHash string) ([]byte, error) {
	docs, err := load("40-deployment.yaml")
	if err != nil {
		return nil, err
	}
	edits := namespaceEdits(docs, opts.Namespace)
	edits = append(edits,
		edit{doc: 0, path: "spec.template.metadata.annotations." + segment(configHashAnnotation), value: configHash},
		edit{doc: 0, path: "spec.template.spec.initContainers.0.image", value: opts.Image},
		edit{doc: 0, path: "spec.template.spec.containers.0.image", value: opts.Image},
	)
	if err := apply(docs, edits); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

// renderServices sets the node ports, or takes them out.
//
// The literal manifest is the ClusterIP form, because that is what a real
// deployment behind an ingress wants and what a reader should meet first. A
// NodePort is the local-cluster case and is added when asked for.
func renderServices(opts Options) ([]byte, error) {
	docs, err := load("50-service.yaml")
	if err != nil {
		return nil, err
	}
	edits := namespaceEdits(docs, opts.Namespace)

	for i, port := range []int{opts.APINodePort, opts.OCINodePort} {
		if port == 0 {
			continue
		}
		edits = append(edits,
			edit{doc: i, path: "spec.type", value: "NodePort"},
			edit{doc: i, path: "spec.ports.0.nodePort", value: port, insert: true},
		)
	}
	if err := apply(docs, edits); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

func renderIngress(opts Options) ([]byte, error) {
	docs, err := load("60-ingress.yaml")
	if err != nil {
		return nil, err
	}
	edits := namespaceEdits(docs, opts.Namespace)
	// The host appears in a Traefik match expression rather than as a value of
	// its own, so it is rewritten as the whole rule.
	rule := fmt.Sprintf("Host(`%s`)", opts.Host)
	edits = append(edits,
		edit{doc: 1, path: "spec.routes.0.match", value: rule},
		edit{doc: 2, path: "spec.routes.0.match", value: rule},
	)
	if err := apply(docs, edits); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}

// Render produces the manifests for a surface.
//
// Files are numbered in apply order. `kubectl apply -f config/` applies a
// directory in lexical order, so the namespace has to sort before the things
// inside it -- ordering that is otherwise invisible and only shows up as a
// failure on a cluster where the namespace does not already exist.
func Render(s *Surface, opts Options) ([]File, error) {
	if s == nil {
		return nil, fmt.Errorf("no surface to render")
	}

	configMap := entries(s.ConfigMapVars())
	secret := entries(s.SecretVars())
	hash := configHash(configMap, secret)

	type step struct {
		path      string
		sensitive bool
		render    func() ([]byte, error)
	}

	steps := []step{
		{path: ConfigDir + "/00-namespace.yaml", render: func() ([]byte, error) { return renderNamespace(opts) }},
		{path: ConfigDir + "/10-service-account.yaml", render: func() ([]byte, error) { return renderServiceAccount(opts) }},
		{path: ConfigDir + "/20-configmap.yaml", render: func() ([]byte, error) { return renderConfigMap(opts, configMap) }},
	}
	if opts.Database == DatabaseInternal {
		steps = append(steps, step{path: ConfigDir + "/30-database.yaml", render: func() ([]byte, error) { return renderDatabase(opts) }})
	}
	steps = append(steps,
		step{path: ConfigDir + "/40-deployment.yaml", render: func() ([]byte, error) { return renderDeployment(opts, hash) }},
		step{path: ConfigDir + "/50-service.yaml", render: func() ([]byte, error) { return renderServices(opts) }},
	)
	if opts.Ingress == IngressTraefik {
		steps = append(steps, step{path: ConfigDir + "/60-ingress.yaml", render: func() ([]byte, error) { return renderIngress(opts) }})
	}
	steps = append(steps, step{
		path: SecretsDir + "/confighub-secret.yaml", sensitive: true,
		render: func() ([]byte, error) { return renderSecret(opts, secret) },
	})

	files := make([]File, 0, len(steps))
	for _, st := range steps {
		content, err := st.render()
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", st.path, err)
		}
		files = append(files, File{Path: st.path, Sensitive: st.sensitive, Content: content})
	}
	return files, nil
}
