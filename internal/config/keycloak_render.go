package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/confighub/sdk/core/third_party/gaby"
)

// Rendering the realm and the Keycloak manifests.
//
// The realm is JSON because Keycloak's importer reads JSON, which is the one
// place here that is not YAML. It gets the same treatment as every other
// manifest regardless: a literal document plus a list of path edits, each
// checked to exist before it is written, so a field renamed in the document
// fails loudly instead of silently producing a realm with something missing.

// jsonEdit sets one value at one dotted path in a parsed JSON document.
//
// The path must already exist. That is the whole value of doing it this way
// rather than with string substitution: an edit and a document that have drifted
// apart say so here, rather than importing a realm where a client has no key.
//
// Segments are escaped with segment() the same way gaby paths are, because
// Keycloak's client attributes are dotted names -- jwks.string is one key, not
// two -- and a path language that splits on dots has to be told which ones are
// separators.
func jsonEdit(doc map[string]any, path string, value any) error {
	segments := strings.Split(path, ".")
	for i := range segments {
		segments[i] = strings.ReplaceAll(segments[i], "~1", ".")
	}
	cursor := any(doc)

	for i, segment := range segments {
		switch node := cursor.(type) {
		case map[string]any:
			if i == len(segments)-1 {
				if _, ok := node[segment]; !ok {
					return fmt.Errorf("no path %q in the realm: it and this edit have drifted apart", path)
				}
				node[segment] = value
				return nil
			}
			next, ok := node[segment]
			if !ok {
				return fmt.Errorf("no path %q in the realm: %q is missing", path, strings.Join(segments[:i+1], "."))
			}
			cursor = next
		case []any:
			index, err := indexOf(segment)
			if err != nil || index >= len(node) {
				return fmt.Errorf("no path %q in the realm: %q is not an index into a list of %d", path, segment, len(node))
			}
			if i == len(segments)-1 {
				node[index] = value
				return nil
			}
			cursor = node[index]
		default:
			return fmt.Errorf("no path %q in the realm: %q is not a map or a list", path, strings.Join(segments[:i], "."))
		}
	}
	return fmt.Errorf("empty path")
}

func indexOf(segment string) (int, error) {
	var index int
	_, err := fmt.Sscanf(segment, "%d", &index)
	return index, err
}

// renderRealm produces the realm Keycloak imports on first start.
//
// The client ids are written rather than assumed even though the literal
// document already carries the defaults, so that an install which renamed them
// gets a realm that matches its own configuration rather than one that silently
// keeps the default.
func renderRealm(k *Keycloak, clientJWKS string) ([]byte, error) {
	content, err := manifests.ReadFile("manifests/keycloak-realm.json")
	if err != nil {
		return nil, err
	}
	var realm map[string]any
	if err := json.Unmarshal(content, &realm); err != nil {
		return nil, fmt.Errorf("parsing the realm: %w", err)
	}

	edits := []struct {
		path  string
		value any
	}{
		{"realm", k.Realm},
		{"organizations.0.name", k.Org},
		{"organizations.0.alias", k.Org},
		{"organizations.0.domains.0.name", k.OrgDomain},

		{"clients.0.clientId", k.ClientID},
		// The public half of the key the server signs with. Everything else
		// about this install is recoverable; a realm importing the wrong key
		// here is an instance that cannot talk to its own identity provider.
		{"clients.0.attributes." + segment("jwks.string"), clientJWKS},

		{"clients.1.clientId", k.DeviceClientID},

		// The UI's own client. Its redirect URI is exact -- the UI's origin --
		// because a public client is secured by PKCE and by having nowhere else
		// to send a code. The audience
		// is what the server pins on a token it will exchange.
		{"clients.2.clientId", k.UIClientID},
		{"clients.2.redirectUris", []any{k.UIRedirectURI}},
		{"clients.2.webOrigins", []any{strings.TrimSuffix(k.UIRedirectURI, "/")}},
		{"clients.2.protocolMappers.0.config." + segment("included.custom.audience"), k.Audience()},

		{"users.0.username", "service-account-" + k.ClientID},
		{"users.0.serviceAccountClientId", k.ClientID},
	}
	for _, e := range edits {
		if err := jsonEdit(realm, e.path, e.value); err != nil {
			return nil, err
		}
	}

	out, err := json.MarshalIndent(realm, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// expectEnv guards an edit addressed by index into a container's env list.
//
// gaby addresses arrays by index, and apply only checks that the path exists --
// which it still does after someone reorders the list, so the edit would land on
// the wrong variable and produce a Keycloak that ignores its own hostname while
// announcing an admin username as its issuer. Naming what is expected to be
// there turns a silent mislanding into a refusal.
func expectEnv(doc *gaby.YamlDoc, path, want string) error {
	at := doc.Path(path)
	if at == nil {
		return fmt.Errorf("no %s in the Keycloak manifest: it and this renderer have drifted apart", path)
	}
	if got, _ := at.Data().(string); got != want {
		return fmt.Errorf("expected %s to be %q, found %q: the Keycloak manifest and this renderer have drifted apart", path, want, got)
	}
	return nil
}

// renderKeycloak produces the realm ConfigMap, the Service, and the StatefulSet.
func renderKeycloak(opts Options, clientKey string) ([]byte, error) {
	k := opts.Keycloak
	docs, err := load("25-keycloak.yaml")
	if err != nil {
		return nil, err
	}

	clientJWKS, err := ClientJWKS(clientKey)
	if err != nil {
		return nil, err
	}
	realm, err := renderRealm(k, clientJWKS)
	if err != nil {
		return nil, err
	}

	const (
		realmDoc = 0
		svcDoc   = 1
		setDoc   = 2
	)

	const (
		envUser     = "spec.template.spec.containers.0.env.0"
		envHostname = "spec.template.spec.containers.0.env.2"
	)
	if err := expectEnv(docs[setDoc], envUser+".name", "KC_BOOTSTRAP_ADMIN_USERNAME"); err != nil {
		return nil, err
	}
	if err := expectEnv(docs[setDoc], envHostname+".name", "KC_HOSTNAME"); err != nil {
		return nil, err
	}

	edits := namespaceEdits(docs, opts.Namespace)
	edits = append(edits,
		edit{doc: realmDoc, path: "data." + segment("confighub-realm.json"), value: string(realm)},

		edit{doc: setDoc, path: "spec.template.spec.containers.0.image", value: k.Image},
		edit{doc: setDoc, path: "spec.volumeClaimTemplates.0.spec.resources.requests.storage", value: k.StorageSize},
		edit{doc: setDoc, path: envUser + ".value", value: k.AdminUser},
		// KC_HOSTNAME is the browser's address. Keycloak reports it as the issuer
		// and in every frontchannel URL no matter which address a request
		// arrived on, which is what makes one discovery document serve both the
		// browser and the server.
		edit{doc: setDoc, path: envHostname + ".value", value: k.PublicURL},
	)
	if k.NodePort != 0 {
		edits = append(edits,
			edit{doc: svcDoc, path: "spec.type", value: "NodePort"},
			edit{doc: svcDoc, path: "spec.ports.0.nodePort", value: k.NodePort, insert: true},
		)
	}
	if err := apply(docs, edits); err != nil {
		return nil, err
	}
	return docs.Bytes(), nil
}
