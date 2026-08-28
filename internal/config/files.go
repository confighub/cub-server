// Package k8s renders a resolved config surface into Kubernetes manifests.
//
// The resource set is a namespace, a service account, a ConfigMap, a Secret, a
// Deployment and its Services, and optionally a bundled database and an
// ingress.
//
// The Deployment reads its secrets with `envFrom: secretRef`, so a deployment
// that populates that Secret from a secret manager instead can drop the
// generated one and change nothing else.
//
// The manifests themselves are literal YAML in manifests/, and what varies per
// install is applied to them as a list of path edits. See edit.go for why that
// is worth more than a template or a typed API object.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// File is one rendered artifact and where it belongs relative to the output
// directory.
type File struct {
	Path string

	// Sensitive marks output that must not be committed. It drives nothing about
	// the content -- it tells the caller which files need care.
	Sensitive bool

	Content []byte
}

const (
	// ConfigDir holds manifests that carry no secrets. Committable, diffable,
	// and usable as a fixture.
	ConfigDir = "config"

	// SecretsDir holds the Secret. Separate from the manifests precisely so that
	// committing the manifests cannot commit a credential.
	SecretsDir = "secrets"
)

// entry is one key/value pair with the reason it exists, so the rendered
// manifest carries the explanation rather than requiring a reader to go and find
// the Go file that reads the key.
type entry struct {
	Key   string
	Value string
	Doc   string
}

func entries(vars []Var) []entry {
	out := make([]entry, 0, len(vars))
	for _, v := range vars {
		out = append(out, entry{Key: v.Name, Value: v.Value, Doc: v.Doc})
	}
	// Sorted so that regenerating with no changes produces an identical file.
	// Otherwise every re-run is a spurious diff and nobody reads the real ones.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// configHash digests the configuration the Deployment consumes with envFrom.
//
// Over names and values from the resolved surface rather than over the rendered
// YAML, so that a comment change or a reordering does not read as a
// configuration change and restart a healthy server. entries() has already
// sorted them, which is what makes this stable across runs.
//
// Truncated to 32 hex characters: this is a change detector, not a security
// boundary -- nothing trusts it, and the values it digests are already in the
// objects it annotates.
func configHash(configMap, secret []entry) string {
	h := sha256.New()
	for _, group := range [][]entry{configMap, secret} {
		for _, e := range group {
			// Length-prefixed, so that {"AB": "C"} and {"A": "BC"} cannot digest
			// to the same thing.
			fmt.Fprintf(h, "%d:%s=%d:%s\n", len(e.Key), e.Key, len(e.Value), e.Value)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
