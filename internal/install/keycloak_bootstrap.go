package install

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// The one step that needs an administrator of the whole Keycloak.
//
// OAuth clients belong to a realm, and so does everything the ConfigHub server
// can do: its client's service account administers its own realm and no other.
// That is enough for everything ConfigHub needs from Keycloak -- except creating
// the realm, and the client, in the first place. So that is all this does, and
// it is the only command that asks for a master-realm credential. Everything
// after it (`configure`, `idp`) signs in as the server's client, with the
// server's key.
//
// Splitting it out means the two can be different people: whoever administers
// the company's Keycloak runs this once and hands over the key it writes, and
// whoever runs ConfigHub does the rest with a credential that reaches one
// realm. The key has to reach the ConfigHub side anyway: it is the server's
// credential, and goes into the server's Secret.

// BootstrapOptions is what `cub server keycloak bootstrap` needs.
type BootstrapOptions struct {
	// Keycloak's PublicURL, Realm and ClientID are what this creates and how
	// tokens will name it.
	Keycloak config.Keycloak

	// AdminURL is where this command reaches Keycloak, when that is not
	// PublicURL.
	AdminURL string
	Admin    KeycloakAdminCredential

	// OutDir is where the server's key is written; ClientKeyFile is a key to use
	// instead of the one there or a new one.
	OutDir        string
	ClientKeyFile string
}

// RunBootstrap creates the realm and the server's client, or brings an
// existing one's client to the key this writes.
func RunBootstrap(ctx context.Context, u UI, in io.Reader, b *BootstrapOptions) error {
	k := &b.Keycloak
	if k.PublicURL == "" {
		return fmt.Errorf("--keycloak-url is required: where browsers reach Keycloak, and so the issuer of every token")
	}
	for name, raw := range map[string]string{"--keycloak-url": k.PublicURL, "--admin-url": b.AdminURL} {
		if raw == "" {
			continue
		}
		if parsed, err := url.Parse(raw); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%s %q is not an http(s) URL", name, raw)
		}
	}
	k.PublicURL = strings.TrimSuffix(k.PublicURL, "/")
	if k.SSLRequired == "" {
		k.SSLRequired = "external"
	}
	k.Defaults()
	if b.AdminURL == "" {
		b.AdminURL = k.PublicURL
	}
	if b.OutDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine a home directory for the server's key: %w", err)
		}
		b.OutDir = keycloakWorkDir(home, k.PublicURL, k.Realm)
	}
	if err := resolveAdminCredential(&b.Admin, in, u.Out); err != nil {
		return err
	}

	keyPath := filepath.Join(b.OutDir, clientKeyFile)
	clientKey, generated, err := bootstrapClientKey(b.ClientKeyFile, keyPath)
	if err != nil {
		return err
	}
	clientJWKS, err := config.ClientJWKS(clientKey)
	if err != nil {
		return err
	}
	realm, err := bootstrapRealm(k, clientJWKS)
	if err != nil {
		return err
	}

	u.step("Connecting to Keycloak as an administrator")
	admin, err := newKeycloakAdminAt(ctx, b.AdminURL, k.Realm, b.Admin)
	if err != nil {
		return err
	}
	u.detail("%s", b.AdminURL)
	if err := checkKeycloakVersion(ctx, u, admin); err != nil {
		return err
	}

	u.step("Bootstrapping realm %q", k.Realm)
	exists, err := admin.realmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if err := admin.createRealm(ctx, realm); err != nil {
			return fmt.Errorf("creating realm %q: %w", k.Realm, err)
		}
		u.detail("created, with the server's client %q", k.ClientID)
	} else {
		u.detail("exists")
		for _, client := range realmClients(realm) {
			changed, err := admin.reconcileClient(ctx, client)
			if err != nil {
				return err
			}
			if changed {
				u.detail("client %s: set to this key", client["clientId"])
			}
		}
		for owner, grants := range realmServiceAccountGrants(realm) {
			for roleClient, roles := range grants {
				if err := admin.grantClientRoles(ctx, owner, roleClient, roles); err != nil {
					return fmt.Errorf("granting %s %s roles: %w", owner, roleClient, err)
				}
			}
		}
	}

	// Written before it is tried: Keycloak now holds the public half, so a
	// failure below has to leave the private half where a re-run finds it.
	if err := os.MkdirAll(b.OutDir, 0o700); err != nil {
		return err
	}
	if err := writeFile(keyPath, []byte(clientKey+"\n"), 0o600); err != nil {
		return err
	}

	// The key, used the way configure and the server will: sign in as the
	// server's client and read the realm.
	u.step("Checking the server's key")
	realmAdmin, err := newKeycloakRealmAdmin(ctx, b.AdminURL, k.Issuer(), k.Realm, k.ClientID, clientKey)
	if err != nil {
		return err
	}
	if _, err := realmAdmin.realmExists(ctx); err != nil {
		return fmt.Errorf("the server's client signed in but cannot read the realm: %w", err)
	}
	u.detail("it signs in to %q and administers it", k.Realm)

	lines := []string{
		"realm        " + k.Realm,
		"issuer       " + k.Issuer(),
		"server key   " + keyPath,
		"",
		"That key is the server's credential, and the only thing anyone needs to set",
		"up the rest of this realm: it reaches this realm and nothing else on this",
		"Keycloak. Hand it to whoever runs ConfigHub, who continues with:",
		"",
		"  cub server keycloak configure --keycloak-url " + k.PublicURL + " --ui-url <where the UI is>",
		"",
		"Pass the key with --client-key-file if it is not in " + b.OutDir + ".",
	}
	if generated && exists {
		lines = append(lines, "",
			"A new key was made and Keycloak switched to it. A server already set up",
			"with this realm cannot reach Keycloak until its Secret has the new key.")
	}
	u.section("Realm bootstrapped.", lines...)
	return nil
}

// bootstrapClientKey is the key passed in, else the one a previous run wrote,
// else a new one.
func bootstrapClientKey(passed, kept string) (key string, generated bool, err error) {
	if passed != "" {
		key, err := readClientKey(passed)
		return key, false, err
	}
	key, err = readClientKey(kept)
	switch {
	case err == nil:
		return key, false, nil
	case os.IsNotExist(err):
		key, err := config.GenerateClientKey()
		return key, true, err
	default:
		return "", false, err
	}
}

// bootstrapRealm is the realm document cut down to what bootstrap creates: the
// realm's own settings, the server's client, and that client's service
// account with its roles. The other clients, the organization and the roles
// are configure's, which can make them as the server's client.
func bootstrapRealm(k *config.Keycloak, clientJWKS string) (map[string]any, error) {
	doc, err := config.RenderRealm(k, clientJWKS)
	if err != nil {
		return nil, err
	}
	var realm map[string]any
	if err := json.Unmarshal(doc, &realm); err != nil {
		return nil, err
	}
	var clients []any
	for _, c := range realmClients(realm) {
		if c["clientId"] == k.ClientID {
			clients = append(clients, c)
		}
	}
	var users []any
	list, _ := realm["users"].([]any)
	for _, u := range list {
		if user, ok := u.(map[string]any); ok && user["serviceAccountClientId"] == k.ClientID {
			users = append(users, user)
		}
	}
	if len(clients) != 1 || len(users) != 1 {
		return nil, fmt.Errorf("the realm document has no client %q with a service account", k.ClientID)
	}
	realm["clients"] = clients
	realm["users"] = users
	delete(realm, "organizations")
	return realm, nil
}
