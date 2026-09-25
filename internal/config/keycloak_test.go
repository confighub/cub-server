package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func testKeycloak() *Keycloak {
	k := &Keycloak{
		PublicURL:     "http://localhost:32182",
		RedirectURI:   "http://localhost:32180/auth/callback",
		UIRedirectURI: "http://localhost:32180/",
	}
	k.Defaults()
	return k
}

// The realm's whole job is to describe a client Keycloak will accept this
// server's assertions for, and the key is the part of it nothing else can
// supply.
func TestRealmCarriesTheClientKey(t *testing.T) {
	privateJWK, err := GenerateClientKey()
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := ClientJWKS(privateJWK)
	if err != nil {
		t.Fatal(err)
	}

	content, err := renderRealm(testKeycloak(), jwks)
	if err != nil {
		t.Fatalf("rendering the realm: %v", err)
	}

	var realm struct {
		Realm         string `json:"realm"`
		Organizations []struct {
			Name string `json:"name"`
		} `json:"organizations"`
		Clients []struct {
			ClientID                string            `json:"clientId"`
			PublicClient            bool              `json:"publicClient"`
			ClientAuthenticatorType string            `json:"clientAuthenticatorType"`
			ServiceAccountsEnabled  bool              `json:"serviceAccountsEnabled"`
			RedirectURIs            []string          `json:"redirectUris"`
			Attributes              map[string]string `json:"attributes"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(content, &realm); err != nil {
		t.Fatalf("the rendered realm is not JSON, which is the only thing Keycloak imports: %v", err)
	}

	if realm.Realm != DefaultKeycloakRealm {
		t.Errorf("realm is %q, want %q", realm.Realm, DefaultKeycloakRealm)
	}
	// A user who is not in an organization can sign in and is then told their
	// account is pending approval, so the realm having one is load-bearing.
	if len(realm.Organizations) != 1 || realm.Organizations[0].Name != DefaultKeycloakOrg {
		t.Errorf("organizations are %+v, want one named %q", realm.Organizations, DefaultKeycloakOrg)
	}
	if len(realm.Clients) != 3 {
		t.Fatalf("got %d clients, want the server's, the CLI's and the UI's", len(realm.Clients))
	}

	server := realm.Clients[0]
	if server.ClientAuthenticatorType != "client-jwt" {
		t.Errorf("the server's client authenticates with %q, want client-jwt: any other value means it needs a secret",
			server.ClientAuthenticatorType)
	}
	if !server.ServiceAccountsEnabled {
		t.Error("the server's client has no service account, so it cannot reach the admin API without a password")
	}
	if server.PublicClient {
		t.Error("the server's client is public, which means Keycloak would not authenticate it at all")
	}
	if got := server.Attributes["use.jwks.string"]; got != "true" {
		t.Errorf("use.jwks.string is %q, want true: without it Keycloak ignores the key below", got)
	}

	// The key itself, and the kid: Keycloak selects by kid, and reports a
	// mismatch as a signature failure rather than as a missing key.
	if server.Attributes["jwks.string"] != jwks {
		t.Fatalf("the realm carries a different key set than the one generated:\n got %q\nwant %q",
			server.Attributes["jwks.string"], jwks)
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(server.Attributes["jwks.string"]), &set); err != nil {
		t.Fatalf("the key set is not JSON: %v", err)
	}
	if len(set.Keys) != 1 || set.Keys[0]["kid"] == "" {
		t.Fatalf("key set has no kid: %+v", set.Keys)
	}
	if d := set.Keys[0]["d"]; d != "" {
		t.Fatal("the realm carries the PRIVATE key: Keycloak must only ever hold the public half")
	}

	if want := "http://localhost:32180/*"; len(server.RedirectURIs) != 1 || server.RedirectURIs[0] != want {
		t.Errorf("redirect URIs are %v, want [%s]", server.RedirectURIs, want)
	}

	cli := realm.Clients[1]
	if !cli.PublicClient {
		t.Error("the CLI's client is confidential, but it runs on a user's machine and can hold no credential")
	}

	// The UI is a browser app: public, so PKCE and one exact redirect URI are
	// what stand in for a secret. A wildcard here would hand a code to anywhere
	// under the origin.
	ui := realm.Clients[2]
	if !ui.PublicClient {
		t.Error("the UI's client is confidential, but it runs in a browser and can hold no secret")
	}
	if ui.Attributes["pkce.code.challenge.method"] != "S256" {
		t.Errorf("the UI's client enforces PKCE %q, want S256", ui.Attributes["pkce.code.challenge.method"])
	}
	if want := "http://localhost:32180/"; len(ui.RedirectURIs) != 1 || ui.RedirectURIs[0] != want {
		t.Errorf("the UI's redirect URIs are %v, want exactly [%s]", ui.RedirectURIs, want)
	}
}

// A renamed client has to reach the realm, or the server authenticates as one
// client against a realm that describes another.
func TestRealmFollowsRenamedClients(t *testing.T) {
	k := testKeycloak()
	k.Realm = "acme"
	k.ClientID = "acme-server"
	k.DeviceClientID = "acme-cli"
	k.Org = "acme-org"

	content, err := renderRealm(k, `{"keys":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	var realm struct {
		Realm   string `json:"realm"`
		Clients []struct {
			ClientID string `json:"clientId"`
		} `json:"clients"`
		Users []struct {
			Username               string `json:"username"`
			ServiceAccountClientID string `json:"serviceAccountClientId"`
		} `json:"users"`
	}
	if err := json.Unmarshal(content, &realm); err != nil {
		t.Fatal(err)
	}

	if realm.Realm != "acme" {
		t.Errorf("realm is %q, want acme", realm.Realm)
	}
	if realm.Clients[0].ClientID != "acme-server" || realm.Clients[1].ClientID != "acme-cli" {
		t.Errorf("clients are %+v, want the renamed ones", realm.Clients)
	}
	// Keycloak names a client's service account after the client. A username
	// that still says "confighub" is a role grant that lands on nothing.
	if realm.Users[0].Username != "service-account-acme-server" {
		t.Errorf("service account user is %q, want service-account-acme-server", realm.Users[0].Username)
	}
	if realm.Users[0].ServiceAccountClientID != "acme-server" {
		t.Errorf("service account client is %q, want acme-server", realm.Users[0].ServiceAccountClientID)
	}
}

// The point of editing by path is that a path which is not there is an error
// rather than a silent no-op. Keycloak's client attributes are dotted names, so
// the escaping is part of that guarantee rather than a detail of it.
func TestJSONEditChecksPaths(t *testing.T) {
	doc := map[string]any{
		"clients": []any{
			map[string]any{
				"attributes": map[string]any{"jwks.string": ""},
			},
		},
	}

	if err := jsonEdit(doc, "clients.0.attributes."+segment("jwks.string"), "set"); err != nil {
		t.Fatalf("escaped dotted key: %v", err)
	}
	attrs := doc["clients"].([]any)[0].(map[string]any)["attributes"].(map[string]any)
	if attrs["jwks.string"] != "set" {
		t.Fatalf("attributes are %+v, want jwks.string set", attrs)
	}

	// Unescaped, the key name is read as two segments and nothing matches.
	// This is the shape of the bug the check exists to catch.
	err := jsonEdit(doc, "clients.0.attributes.jwks.string", "set")
	if err == nil {
		t.Fatal("an unescaped dotted key was accepted, so the path check is not doing anything")
	}
	if !strings.Contains(err.Error(), "drifted apart") && !strings.Contains(err.Error(), "missing") {
		t.Errorf("error does not say what is wrong: %v", err)
	}

	for _, path := range []string{"nope", "clients.9.attributes", "clients.0.nope.deeper"} {
		if err := jsonEdit(doc, path, "x"); err == nil {
			t.Errorf("path %q was accepted but does not exist", path)
		}
	}
}

// An instance with no identity provider must emit none of these: the server
// treats a realm, auth URL and redirect URI as all-or-nothing and refuses to
// start on a partial set.
func TestKeycloakVarsOnlyWhenConfigured(t *testing.T) {
	keycloakNames := []string{
		"KEYCLOAK_REALM", "KEYCLOAK_AUTH_URL", "KEYCLOAK_INTERNAL_URL",
		"KEYCLOAK_REDIRECT_URI", "KEYCLOAK_CLIENT_ID", "KEYCLOAK_DEVICE_CLIENT_ID",
		"CONFIGHUB_UI_OAUTH_CLIENT_ID", "CONFIGHUB_IDP_ISSUER", "CONFIGHUB_IDP_AUDIENCE",
		KeycloakClientKeyEnv,
	}

	without := Options{Namespace: "confighub", Image: "img"}
	without.Defaults()
	s, err := Build(without, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range keycloakNames {
		if s.Get(name) != "" {
			t.Errorf("%s is set on an instance with no identity provider", name)
		}
	}

	with := Options{Namespace: "confighub", Image: "img", Keycloak: testKeycloak()}
	with.Defaults()
	s, err = Build(with, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range keycloakNames {
		if s.Get(name) == "" {
			t.Errorf("%s is missing from an instance that has an identity provider", name)
		}
	}
	// The local administrator survives. The server keys break-glass on this
	// variable rather than on whether an identity provider exists, so adding one
	// must not take it away.
	if s.Get(AdminJWKEnv) == "" {
		t.Error("the local administrator key was dropped when Keycloak was added")
	}
	if want := "http://confighub-keycloak.confighub.svc:8080"; s.Get("KEYCLOAK_INTERNAL_URL") != want {
		t.Errorf("internal URL is %q, want %q", s.Get("KEYCLOAK_INTERNAL_URL"), want)
	}

	// The issuer is the browser's address, never the dialed one: it is what
	// tokens name, and a token whose issuer does not match is rejected.
	if want := "http://localhost:32182/realms/confighub"; s.Get("CONFIGHUB_IDP_ISSUER") != want {
		t.Errorf("issuer is %q, want %q", s.Get("CONFIGHUB_IDP_ISSUER"), want)
	}
	// The audience the server pins has to be the one the UI client stamps, or
	// every exchange fails on the audience check.
	if s.Get("CONFIGHUB_IDP_AUDIENCE") != with.Keycloak.Audience() {
		t.Errorf("audience is %q, want %q", s.Get("CONFIGHUB_IDP_AUDIENCE"), with.Keycloak.Audience())
	}
}

// Keycloak's admin password must not be in the Secret the server reads.
//
// The server's Deployment takes that Secret whole with envFrom, so a key in it
// is in the server's environment whether the server reads it or not. This one is
// an admin of the master realm -- authority over every realm, including the one
// that governs the server -- so putting it there would hand a compromised server
// pod the identity provider it authenticates against.
func TestKeycloakAdminPasswordIsNotInTheServersSecret(t *testing.T) {
	opts := Options{Namespace: "confighub", Image: "img", Keycloak: testKeycloak()}
	opts.Defaults()
	s, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, v := range s.SecretVars() {
		if v.Name == keycloakBootstrapPasswordVar {
			t.Fatalf("%s is in the Secret the server consumes with envFrom", v.Name)
		}
	}

	var found bool
	for _, v := range s.KeycloakSecretVars() {
		if v.Name == keycloakBootstrapPasswordVar {
			found = true
			if v.Value == "" {
				t.Error("the admin password is empty, so Keycloak would come up with no admin at all")
			}
		}
	}
	if !found {
		t.Fatalf("%s is in neither Secret", keycloakBootstrapPasswordVar)
	}

	// And the server's own credential stays where the server can read it.
	var serverKey bool
	for _, v := range s.SecretVars() {
		if v.Name == KeycloakClientKeyEnv {
			serverKey = true
		}
	}
	if !serverKey {
		t.Error("the client key is not in the server's Secret, so the server cannot authenticate")
	}
}

// Re-running must not rotate the client key: Keycloak holds the public half, and
// a new private half is a server that cannot authenticate to its own realm.
func TestClientKeyIsPreservedAcrossRuns(t *testing.T) {
	opts := Options{Namespace: "confighub", Image: "img", Keycloak: testKeycloak()}
	opts.Defaults()

	first, err := Build(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := first.Get(KeycloakClientKeyEnv)
	if key == "" {
		t.Fatal("no client key generated")
	}

	second, err := Build(opts, Preserved{KeycloakClientKeyEnv: key})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Get(KeycloakClientKeyEnv); got != key {
		t.Error("the client key was rotated on a re-run; Keycloak still holds the old public half")
	}
}

// The public half in the realm has to be the half of the key the server signs
// with, including after a re-run that preserved the private one.
func TestClientJWKSMatchesThePrivateKey(t *testing.T) {
	privateJWK, err := GenerateClientKey()
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := ClientJWKS(privateJWK)
	if err != nil {
		t.Fatal(err)
	}

	var private map[string]string
	if err := json.Unmarshal([]byte(privateJWK), &private); err != nil {
		t.Fatal(err)
	}
	var set struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(jwks), &set); err != nil {
		t.Fatal(err)
	}

	if set.Keys[0]["x"] != private["x"] {
		t.Error("the published key is not the public half of the private one")
	}
	if set.Keys[0]["alg"] != "EdDSA" {
		t.Errorf("alg is %q, want EdDSA", set.Keys[0]["alg"])
	}
	// Deriving it twice must agree, since one copy goes to Keycloak and the
	// other is computed by the server when it signs.
	again, err := ClientJWKS(privateJWK)
	if err != nil {
		t.Fatal(err)
	}
	if again != jwks {
		t.Error("deriving the key set twice produced different results")
	}
}
