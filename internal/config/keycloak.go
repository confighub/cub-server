package config

import (
	"encoding/json"
	"fmt"

	"github.com/confighub/sdk/core/jwk"
)

// The bundled identity provider.
//
// An install without this is complete and usable: the bootstrap administrator
// signs in with a key, and that is the whole identity story. Keycloak is the
// second chapter, for when an instance needs more than one person in it.
//
// It is additive. The local administrator keeps working afterwards -- the server
// keys that on CONFIGHUB_LOCAL_ADMIN_JWK being set rather than on whether an
// identity provider exists -- so what this adds is a second way in, not a
// replacement for the first. That first way is then the break-glass credential,
// which is worth having precisely because an identity provider is a thing that
// can break.
//
// # NO PASSWORD AND NO SHARED SECRET REACH THE SERVER
//
// The server authenticates to Keycloak by signing an RFC 7523 assertion with a
// key generated here. Keycloak holds only the public half, as an inline JWKS on
// the client, and the client's service account carries the realm-management
// roles the server needs. So the one credential in the server's environment is a
// private key, and a compromised server pod has exactly the authority that
// service account was granted in that one realm.
//
// # TWO ADDRESSES
//
// The browser reaches Keycloak on a NodePort published on the host; the server
// reaches it at a cluster-internal service name. They are different strings and
// on a laptop there is no third one that works from both. KEYCLOAK_AUTH_URL is
// the browser's, and is therefore the issuer in every token; KEYCLOAK_INTERNAL_URL
// is the server's. Keycloak is told the same thing through KC_HOSTNAME and
// KC_HOSTNAME_BACKCHANNEL_DYNAMIC, which is what makes it report a fixed
// frontchannel and a dynamic backchannel in one discovery document.
const (
	// KeycloakClientKeyEnv is the server's credential: the private half of the
	// key Keycloak holds the public half of.
	KeycloakClientKeyEnv = "KEYCLOAK_CLIENT_PRIVATE_KEY_JWK"

	// keycloakBootstrapPasswordVar is Keycloak's own temporary admin, for
	// reaching its console to add people. The server never reads it; it lives in
	// the surface so one generated value reaches the StatefulSet and survives a
	// re-run, the same reason POSTGRES_PASSWORD is here.
	//
	// Deliberately not KEYCLOAK_ADMIN_PASSWORD: the server reads that name, and
	// a value under it would look like a credential it should use.
	keycloakBootstrapPasswordVar = "KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD"

	DefaultKeycloakRealm          = "confighub"
	DefaultKeycloakClientID       = "confighub"
	DefaultKeycloakDeviceClientID = "cub"
	DefaultKeycloakAdminUser      = "kcadmin"

	// DefaultKeycloakOrgDomain is the organization's email domain.
	//
	// Keycloak requires an organization to have at least one -- it refuses to
	// start otherwise, reporting "You must provide at least one domain" -- and
	// uses it to offer membership to users whose email matches. The default is
	// deliberately one nobody owns, so nothing is auto-assigned by accident;
	// --org-domain is worth setting to your own.
	DefaultKeycloakOrgDomain = "confighub.local"

	// DefaultKeycloakOrg is the organization every user joins.
	//
	// ConfigHub resolves a user's organization from their token and sends anyone
	// who has none to a pending-approval page, so a realm with no organization
	// is one where login appears to work and then does not.
	DefaultKeycloakOrg = "default"

	// DefaultKeycloakImage is pinned rather than resolved from the registry the
	// way the server image is. The server's version is this product's; Keycloak's
	// is a dependency, and picking up whatever is newest would make an install
	// depend on a release nobody here has run.
	DefaultKeycloakImage = "quay.io/keycloak/keycloak:26.1.5"
)

// Keycloak describes the bundled identity provider for one instance.
type Keycloak struct {
	// PublicURL is where a browser reaches Keycloak, and so the issuer of every
	// token it mints.
	PublicURL string

	// RedirectURI is where Keycloak sends the browser back, which is the
	// ConfigHub UI's callback.
	RedirectURI string

	Realm          string
	ClientID       string
	DeviceClientID string

	// Org is the organization users are members of. See DefaultKeycloakOrg.
	Org string

	// OrgDomain is that organization's email domain. See DefaultKeycloakOrgDomain.
	OrgDomain string

	AdminUser   string
	Image       string
	NodePort    int
	StorageSize string
}

// Defaults fills in what the caller did not choose.
func (k *Keycloak) Defaults() {
	if k.Realm == "" {
		k.Realm = DefaultKeycloakRealm
	}
	if k.ClientID == "" {
		k.ClientID = DefaultKeycloakClientID
	}
	if k.DeviceClientID == "" {
		k.DeviceClientID = DefaultKeycloakDeviceClientID
	}
	if k.Org == "" {
		k.Org = DefaultKeycloakOrg
	}
	if k.OrgDomain == "" {
		k.OrgDomain = DefaultKeycloakOrgDomain
	}
	if k.AdminUser == "" {
		k.AdminUser = DefaultKeycloakAdminUser
	}
	if k.Image == "" {
		k.Image = DefaultKeycloakImage
	}
	if k.StorageSize == "" {
		k.StorageSize = "1Gi"
	}
}

// Validate rejects what cannot produce a working install.
func (k *Keycloak) Validate() error {
	if k.PublicURL == "" {
		return fmt.Errorf("the browser-facing Keycloak URL is required: it is the issuer of every token")
	}
	if k.RedirectURI == "" {
		return fmt.Errorf("the redirect URI is required: it is where Keycloak sends the browser back")
	}
	if k.NodePort != 0 && (k.NodePort < 30000 || k.NodePort > 32767) {
		return fmt.Errorf("--keycloak-node-port %d is outside the NodePort range 30000-32767", k.NodePort)
	}
	return nil
}

// ServiceName is the Kubernetes Service, and so the host the server dials. It
// is a constant rather than an option because the manifest names it too, and
// two spellings of one name is a way for them to disagree.
const KeycloakServiceName = "confighub-keycloak"

// InternalURL is where the server reaches Keycloak from inside the cluster.
//
// Derived from the service rather than configured, so it cannot name something
// the manifests did not create.
func (k *Keycloak) InternalURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc:8080", KeycloakServiceName, namespace)
}

// GenerateClientKey mints the key the server authenticates to Keycloak with.
//
// No subject is recorded in the key. A ConfigHub user key carries the identity
// it belongs to, because an assertion signed with it has to say who is signing;
// this key's subject is the OAuth client id, which the server is configured with
// separately and which would be a second copy of the same fact here.
func GenerateClientKey() (string, error) {
	pair, err := jwk.GenerateEd25519("")
	if err != nil {
		return "", fmt.Errorf("generating the Keycloak client key: %w", err)
	}
	return string(pair.PrivateJWK), nil
}

// ClientJWKS is the public half of the client key, as the inline key set
// Keycloak reads from the use.jwks.string client attribute.
//
// Derived from the private key on every render rather than stored beside it.
// A re-run preserves the private key like any other generated value, and
// deriving means the realm import cannot come to describe a key the server no
// longer holds.
//
// The kid is what makes this work at all: Keycloak selects the key by it, and an
// assertion whose header names a kid the set does not contain is reported as a
// signature failure, which points at the signature rather than at key selection.
func ClientJWKS(privateJWK string) (string, error) {
	publicJWK, err := PublicJWKFromPrivate([]byte(privateJWK))
	if err != nil {
		return "", fmt.Errorf("deriving the Keycloak client's public key: %w", err)
	}

	kid, err := jwk.Thumbprint(json.RawMessage(publicJWK))
	if err != nil {
		return "", fmt.Errorf("computing the Keycloak client key's thumbprint: %w", err)
	}

	var key map[string]any
	if err := json.Unmarshal([]byte(publicJWK), &key); err != nil {
		return "", err
	}
	key["kid"] = kid
	key["use"] = "sig"
	key["alg"] = "EdDSA"

	set, err := json.Marshal(map[string]any{"keys": []any{key}})
	if err != nil {
		return "", err
	}
	return string(set), nil
}
