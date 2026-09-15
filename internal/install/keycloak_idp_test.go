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
		if err := idp.resolve(org); err != nil {
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
		if err := idp.resolve(org); err == nil {
			t.Fatal("accepted no discovery URL")
		}
	})

	t.Run("refuses without credentials", func(t *testing.T) {
		idp := &IdPOptions{DiscoveryURL: "https://acme.okta.com/.well-known/openid-configuration"}
		if err := idp.resolve(org); err == nil {
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
		if err := idp.resolve(bare); err == nil {
			t.Fatal("accepted an organization with no email domain")
		}
	})
}
