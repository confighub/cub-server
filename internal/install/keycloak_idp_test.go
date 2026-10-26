package install

import "testing"

// The alias ends up in a redirect URI the operator has to register at their
// identity provider, so a guess that says nothing -- or says "localhost" -- is
// worse than one they are prompted to replace.
func TestAliasFromDiscoveryURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want string
	}{
		{"https://acme.okta.com/.well-known/openid-configuration", "acme"},
		{"https://login.microsoftonline.com/tenant/v2.0/.well-known/openid-configuration", "microsoftonline"},
		{"https://accounts.google.com/.well-known/openid-configuration", "google"},
		{"https://auth.acme.io/.well-known/openid-configuration", "acme"},
		{"https://acme.eu.auth0.com/.well-known/openid-configuration", "acme"},

		// Nothing distinguishing: better to offer a neutral default than to put
		// the machine this happens to be running on into somebody's SSO config.
		{"http://localhost:8080/realms/master/.well-known/openid-configuration", "sso"},
		{"https://sso.com/.well-known/openid-configuration", "sso"},
		{"", "sso"},
		{"not a url at all", "sso"},
	} {
		if got := aliasFromDiscoveryURL(tc.url); got != tc.want {
			t.Errorf("aliasFromDiscoveryURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// What the flow needs and what it can work out for itself.
func TestIdPOptionsResolve(t *testing.T) {
	org := &keycloakOrganization{Name: "Existing", Domains: []struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	}{{Name: "existing.test"}}}

	t.Run("defaults to the organization already there", func(t *testing.T) {
		idp := &IdPOptions{
			DiscoveryURL: "https://acme.okta.com/.well-known/openid-configuration",
			ClientID:     "id", ClientSecret: "secret",
		}
		if err := idp.resolve(org, false); err != nil {
			t.Fatal(err)
		}
		if idp.OrgName != "Existing" || idp.OrgDomain != "existing.test" {
			t.Errorf("org resolved to %q/%q, want the existing one", idp.OrgName, idp.OrgDomain)
		}
		if idp.Alias != "acme" {
			t.Errorf("alias is %q, want acme", idp.Alias)
		}
	})

	t.Run("refuses without a provider", func(t *testing.T) {
		idp := &IdPOptions{ClientID: "id", ClientSecret: "secret"}
		if err := idp.resolve(org, false); err == nil {
			t.Fatal("accepted no discovery URL")
		}
	})

	t.Run("refuses without credentials", func(t *testing.T) {
		idp := &IdPOptions{DiscoveryURL: "https://acme.okta.com/.well-known/openid-configuration"}
		if err := idp.resolve(org, false); err == nil {
			t.Fatal("accepted no client id or secret")
		}
	})

	// Without a domain nothing matches anyone to the provider, so the provider
	// would be registered and grant membership to nobody.
	t.Run("refuses without an email domain", func(t *testing.T) {
		bare := &keycloakOrganization{Name: "Existing"}
		idp := &IdPOptions{
			DiscoveryURL: "https://acme.okta.com/.well-known/openid-configuration",
			ClientID:     "id", ClientSecret: "secret",
		}
		if err := idp.resolve(bare, false); err == nil {
			t.Fatal("accepted an organization with no email domain")
		}
	})
}

// Values are often LDAP DNs, which are full of '=', so the role is what follows
// the last one; and a role the server does not know would map to nothing.
func TestResolveRoles(t *testing.T) {
	idp := &IdPOptions{
		RoleAttribute: "memberOf",
		RoleMap:       []string{"confighub-admins=admin", "CN=cfg-view,OU=Groups,DC=acme,DC=com=Viewer"},
	}
	if err := idp.resolveRoles(); err != nil {
		t.Fatal(err)
	}
	want := []roleMapping{{"confighub-admins", "admin"}, {"CN=cfg-view,OU=Groups,DC=acme,DC=com", "viewer"}}
	if len(idp.roles) != len(want) || idp.roles[0] != want[0] || idp.roles[1] != want[1] {
		t.Errorf("roles = %v, want %v", idp.roles, want)
	}

	for _, bad := range []IdPOptions{
		{RoleMap: []string{"x=admin"}},                            // no attribute to read it from
		{RoleAttribute: "memberOf", RoleMap: []string{"x=root"}},  // not an org role
		{RoleAttribute: "memberOf", RoleMap: []string{"admin"}},   // no value
		{RoleAttribute: "memberOf", RoleMap: []string{"=admin"}},  // empty value
		{RoleAttribute: "memberOf", RoleMap: []string{"admins="}}, // empty role
	} {
		if err := bad.resolveRoles(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestResolveSAML(t *testing.T) {
	org := &keycloakOrganization{Name: "Acme", Domains: []struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	}{{Name: "acme.com"}}}

	// No client credentials: SAML has none.
	idp := &IdPOptions{SAMLMetadataURL: "https://sso.acme.com/pf/federation_metadata.ping"}
	if err := idp.resolve(org, false); err != nil {
		t.Fatal(err)
	}
	if idp.Alias != "acme" {
		t.Errorf("alias is %q, want acme", idp.Alias)
	}

	both := &IdPOptions{SAMLMetadataURL: "https://a", DiscoveryURL: "https://b", ClientID: "id", ClientSecret: "s"}
	if err := both.resolve(org, false); err == nil {
		t.Error("accepted both a discovery URL and SAML metadata")
	}

	method := &IdPOptions{DiscoveryURL: "https://acme.okta.com/x", ClientID: "id", ClientSecret: "s", ClientAuthMethod: "private_key_jwt"}
	if err := method.resolve(org, false); err == nil {
		t.Error("accepted an unsupported client auth method")
	}
}

// Re-running against a registered provider only changes its mappers, so it
// needs none of the provider's details.
func TestResolveRegistered(t *testing.T) {
	org := &keycloakOrganization{Name: "Acme", Domains: []struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	}{{Name: "acme.com"}}}
	idp := &IdPOptions{Alias: "pingfederate", RoleAttribute: "memberOf", RoleMap: []string{"admins=admin"}}
	if err := idp.resolve(org, true); err != nil {
		t.Fatal(err)
	}
	if err := (&IdPOptions{Alias: "pingfederate"}).resolve(org, false); err == nil {
		t.Error("accepted an unregistered alias with no provider details")
	}
}
