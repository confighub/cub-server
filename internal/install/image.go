package install

import (
	"fmt"
	"strings"
)

// Registry is where an install pulls ConfigHub's own images from: the server
// and the web UI, which every release publishes under the same tag. Both are
// public wherever they are, so no registry credentials are needed for an
// evaluation install.
type Registry struct {
	Server string // server image repository, without a tag
	UI     string // web UI image repository, without a tag
}

// The registries every release is published to. Releases are built on ghcr.io;
// gcr.io carries a copy of each one, for networks that reach Google's registry
// and not GitHub's.
var (
	GHCR = Registry{Server: "ghcr.io/confighubai/confighub", UI: "ghcr.io/confighub/ui"}
	GCR  = Registry{Server: "gcr.io/confighubapp/confighub", UI: "gcr.io/confighubapp/ui"}
)

// ParseRegistry reads --registry: "ghcr", "gcr", or the prefix of a mirror
// holding both images as <prefix>/confighub and <prefix>/ui. Empty is ghcr.
func ParseRegistry(s string) (Registry, error) {
	switch s {
	case "", "ghcr", "ghcr.io":
		return GHCR, nil
	case "gcr", "gcr.io":
		return GCR, nil
	}
	prefix := strings.TrimSuffix(s, "/")
	if !strings.Contains(prefix, "/") && !strings.Contains(prefix, ".") {
		return Registry{}, fmt.Errorf("unknown registry %q: expected ghcr, gcr, or a mirror prefix such as registry.example.com/confighub", s)
	}
	return Registry{Server: prefix + "/confighub", UI: prefix + "/ui"}, nil
}

// registry is the one --registry names. Validate has already refused a bad
// value, so the error cannot happen here.
func (o *Options) registry() Registry {
	r, _ := ParseRegistry(o.Registry)
	return r
}

// registryOf finds the registry a server image came from, so its UI is pulled
// from the same place. An image from anywhere else gets the --registry one.
func (o *Options) registryOf(image string) Registry {
	for _, r := range []Registry{o.registry(), GHCR, GCR} {
		if strings.HasPrefix(image, r.Server+":") {
			return r
		}
	}
	return o.registry()
}
