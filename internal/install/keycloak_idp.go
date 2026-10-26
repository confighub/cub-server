package install

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// Hooking an instance up to the identity provider a company already has.
//
// Two things have to be true before someone at that company can sign in to
// ConfigHub as themselves. Keycloak has to broker to their provider, and the
// person arriving through it has to end up a member of the organization --
// because ConfigHub resolves a user's organization from their token and sends
// anyone who has none to a pending-approval page. An identity provider wired up
// without the second part looks like it works and then does not.
//
// Keycloak joins the two with a domain. An identity provider linked to an
// organization and configured with that organization's email domain makes
// everyone who signs in through it a member. So the organization's domain is not
// cosmetic, and setting it is the first half of this command rather than a
// separate chore someone has to know to do first.

// IdPOptions is what hooking up a provider needs.
type IdPOptions struct {
	// OrgName and OrgDomain are the organization as this company would name it.
	// The domain is the one their email addresses end in: it is what matches
	// people to the provider below.
	OrgName   string
	OrgDomain string

	// DiscoveryURL is an OpenID Connect provider's configuration document.
	// Keycloak reads it and fills in the endpoints, so this replaces naming
	// each one.
	DiscoveryURL string

	ClientID     string
	ClientSecret string

	// ClientAuthMethod is how Keycloak presents the client secret to an OpenID
	// Connect provider: client_secret_post (Keycloak's default) or
	// client_secret_basic, which some providers default to instead.
	ClientAuthMethod string

	// SAMLMetadataURL or SAMLMetadataFile is a SAML provider's metadata, in
	// place of DiscoveryURL. A file is for providers whose metadata is exported
	// rather than served, which is common with PingFederate.
	SAMLMetadataURL  string
	SAMLMetadataFile string

	// The SAML attributes carrying a person's email and name. Keycloak takes
	// the username from the NameID; these fill in the rest, and the email is
	// what makes someone a member of the organization.
	EmailAttribute     string
	FirstNameAttribute string
	LastNameAttribute  string

	// RoleAttribute is the SAML attribute or OIDC claim whose values decide org
	// roles, and RoleMap maps those values to roles, as value=role.
	RoleAttribute string
	RoleMap       []string

	// Alias names the provider inside Keycloak, and appears in the redirect URI
	// the company has to register at their end.
	Alias string

	// Interactive asks for anything not supplied, starting from what this
	// instance already has.
	Interactive bool

	// KeycloakURL, Realm and Admin locate a Keycloak this plugin did not
	// install, the one `keycloak configure` set up. Empty KeycloakURL means the
	// instance's own, found through its generated manifests.
	KeycloakURL string
	Realm       string
	Admin       KeycloakAdminCredential

	roles []roleMapping
}

// roleMapping is one --role-map entry.
type roleMapping struct{ value, role string }

func (idp *IdPOptions) saml() bool {
	return idp.SAMLMetadataURL != "" || idp.SAMLMetadataFile != ""
}

// RunIdP sets up the organization and connects a corporate identity provider.
func RunIdP(ctx context.Context, u UI, in io.Reader, o *Options, idp *IdPOptions) error {
	u.step("Connecting to Keycloak")
	var admin *keycloakAdmin
	if idp.KeycloakURL != "" {
		if idp.Realm == "" {
			idp.Realm = config.DefaultKeycloakRealm
		}
		if err := resolveAdminCredential(&idp.Admin, in, u.Out); err != nil {
			return err
		}
		var err error
		if admin, err = newKeycloakAdminAt(ctx, idp.KeycloakURL, idp.Realm, idp.Admin); err != nil {
			return err
		}
	} else {
		if err := o.Defaults(); err != nil {
			return err
		}
		var err error
		if admin, err = newKeycloakAdmin(ctx, o); err != nil {
			return err
		}
	}
	org, err := admin.organization(ctx)
	if err != nil {
		return err
	}
	u.detail("realm %q, organization %q", admin.realm, org.Name)

	if idp.Interactive {
		if err := interviewIdP(in, u.Out, org, idp); err != nil {
			return err
		}
	}
	// A provider already registered under --alias needs nothing about the
	// provider itself: a re-run is for changing what it maps.
	registered := false
	if idp.Alias != "" {
		var err error
		if registered, err = admin.idpExists(ctx, idp.Alias); err != nil {
			return err
		}
	}
	if err := idp.resolve(org, registered); err != nil {
		return err
	}

	// The organization first. The provider is matched to people by this domain,
	// so setting it afterwards would leave a window where the provider works and
	// membership does not.
	if org.Name != idp.OrgName || org.Domain() != idp.OrgDomain {
		u.step("Setting up the organization")
		if err := admin.updateOrganization(ctx, org, idp.OrgName, idp.OrgDomain); err != nil {
			return fmt.Errorf("updating the organization: %w", err)
		}
		u.detail("%q, email domain %s", idp.OrgName, idp.OrgDomain)
	}

	exists, err := admin.idpExists(ctx, idp.Alias)
	if err != nil {
		return err
	}
	if exists {
		// A re-run, to change what is mapped. The provider's own settings are
		// left as they are: they are what people are signing in with.
		u.step("Updating provider %q", idp.Alias)
		u.detail("already registered; its settings are left as they are, and only its mappers are updated")
	} else {
		if err := registerIdP(ctx, u, admin, idp); err != nil {
			return err
		}
		if err := admin.linkIdPToOrg(ctx, org.ID, idp.Alias); err != nil {
			return fmt.Errorf("linking %q to the organization: %w", idp.Alias, err)
		}
		u.detail("linked to %q", idp.OrgName)
	}

	if err := reconcileIdPMappers(ctx, u, admin, idp, exists); err != nil {
		return err
	}

	uiURL, err := admin.uiURL(ctx)
	if err != nil {
		return err
	}
	if uiURL == "" && idp.KeycloakURL == "" {
		uiURL = o.UIURL()
	}

	at := []string{"Then anyone with an @" + idp.OrgDomain + " address can sign in"}
	if uiURL != "" {
		at = []string{"Then anyone with an @" + idp.OrgDomain + " address can sign in at:", "", "  " + uiURL}
	}
	register := []string{
		"One thing is still owed at their end. Register this redirect URI with the",
		"identity provider, or sign-in will fail on the way back:",
		"",
		"  " + admin.RedirectURI(idp.Alias),
	}
	if idp.saml() {
		register = []string{
			"One thing is still owed at their end. Register Keycloak as a service",
			"provider. Its metadata, with the entity id and the assertion consumer",
			"service URL, is at:",
			"",
			"  " + admin.RedirectURI(idp.Alias) + "/descriptor",
		}
	}
	lines := []string{
		"organization   " + idp.OrgName + "  (" + idp.OrgDomain + ")",
		"provider       " + idp.Alias,
		"",
	}
	lines = append(lines, register...)
	lines = append(lines, "")
	lines = append(lines, at...)
	lines = append(lines,
		"",
		"They are made members of "+idp.OrgName+" on the way through, which is what",
		"ConfigHub needs: a user who belongs to no organization is told their",
		"account is pending approval instead of being signed in.",
	)
	if len(idp.roles) > 0 {
		lines = append(lines, "",
			"Org roles follow "+idp.RoleAttribute+" at every sign-in. Anyone matching none",
			"of the mapped values gets creator, unless given a role in Keycloak.")
	}
	u.section("Identity provider connected.", lines...)
	return nil
}

// registerIdP creates the provider from its discovery document or metadata.
func registerIdP(ctx context.Context, u UI, admin *keycloakAdmin, idp *IdPOptions) error {
	u.step("Reading the provider's configuration")
	var (
		providerID = "oidc"
		cfg        map[string]string
		err        error
	)
	switch {
	case idp.SAMLMetadataFile != "":
		providerID = "saml"
		cfg, err = admin.importIdPConfigFile(ctx, providerID, idp.SAMLMetadataFile)
	case idp.SAMLMetadataURL != "":
		providerID = "saml"
		cfg, err = admin.importIdPConfigFrom(ctx, providerID, idp.SAMLMetadataURL)
	default:
		cfg, err = admin.importIdPConfigFrom(ctx, providerID, idp.DiscoveryURL)
	}
	if err != nil {
		return err
	}
	if providerID == "saml" {
		u.detail("single sign-on at %s", cfg["singleSignOnServiceUrl"])
	} else {
		u.detail("issuer %s", cfg["issuer"])
		cfg["clientId"] = idp.ClientID
		cfg["clientSecret"] = idp.ClientSecret
		cfg["clientAuthMethod"] = idp.ClientAuthMethod
		// Without email the organization's domain matches nobody, and the
		// provider lets people in only to strand them on the pending page.
		cfg["defaultScope"] = "openid email profile"
	}
	// What makes a person who signs in here a member of the organization: the
	// domain their address is matched against, and being sent to this provider
	// when it matches rather than being shown a password form they have no
	// password for.
	cfg["kc.org.domain"] = idp.OrgDomain
	cfg["kc.org.broker.redirect.mode.email-matches"] = "true"
	// The company's provider is the authority on who someone is, so what it
	// says replaces what Keycloak recorded at the last sign-in.
	cfg["syncMode"] = "FORCE"

	u.step("Registering it")
	if err := admin.createIdP(ctx, idp.Alias, idp.OrgName, providerID, cfg); err != nil {
		return fmt.Errorf("creating the identity provider: %w", err)
	}
	return nil
}

// roleMapperPrefix marks the mappers --role-map owns, so a later --role-map
// can remove the ones it no longer lists without touching any made by hand.
const roleMapperPrefix = "confighub-role "

// reconcileIdPMappers adds the attribute and role mappers asked for.
func reconcileIdPMappers(ctx context.Context, u UI, admin *keycloakAdmin, idp *IdPOptions, existed bool) error {
	saml := idp.saml()
	if existed && !saml {
		// Not asked for by flags on a re-run; tell SAML from OIDC by asking.
		providerID, err := admin.idpProviderID(ctx, idp.Alias)
		if err != nil {
			return err
		}
		saml = providerID == "saml"
	}

	var want []keycloakIdPMapper
	if saml {
		for _, attr := range []struct{ from, to string }{
			{idp.EmailAttribute, "email"},
			{idp.FirstNameAttribute, "firstName"},
			{idp.LastNameAttribute, "lastName"},
		} {
			if attr.from == "" {
				continue
			}
			want = append(want, keycloakIdPMapper{
				Name:   attr.to,
				Mapper: "saml-user-attribute-idp-mapper",
				Config: map[string]string{"syncMode": "FORCE", "attribute.name": attr.from, "user.attribute": attr.to},
			})
		}
	}
	for _, m := range idp.roles {
		mapper := keycloakIdPMapper{
			Name:   truncate(roleMapperPrefix+m.role+" <- "+m.value, 250),
			Config: map[string]string{"syncMode": "FORCE", "role": m.role},
		}
		if saml {
			mapper.Mapper = "saml-role-idp-mapper"
			mapper.Config["attribute.name"] = idp.RoleAttribute
			mapper.Config["attribute.value"] = m.value
		} else {
			mapper.Mapper = "oidc-role-idp-mapper"
			mapper.Config["claim"] = idp.RoleAttribute
			mapper.Config["claim.value"] = m.value
		}
		want = append(want, mapper)
	}
	if len(want) == 0 {
		return nil
	}

	u.step("Mapping what the provider sends")
	have, err := admin.idpMappers(ctx, idp.Alias)
	if err != nil {
		return err
	}
	byName := map[string]keycloakIdPMapper{}
	for _, m := range have {
		byName[m.Name] = m
	}
	wanted := map[string]bool{}
	written := 0
	for _, m := range want {
		wanted[m.Name] = true
		m.Alias = idp.Alias
		current, ok := byName[m.Name]
		if ok && current.Mapper == m.Mapper && maps.Equal(pick(current.Config, m.Config), m.Config) {
			continue
		}
		if ok {
			m.ID = current.ID
		}
		if err := admin.saveIdPMapper(ctx, idp.Alias, m); err != nil {
			return fmt.Errorf("mapper %q: %w", m.Name, err)
		}
		u.detail("%s", m.Name)
		written++
	}
	// Only when roles were mapped this time: a re-run that maps nothing else
	// should not read as "map no roles".
	if len(idp.roles) > 0 {
		for _, m := range have {
			if strings.HasPrefix(m.Name, roleMapperPrefix) && !wanted[m.Name] {
				if err := admin.deleteIdPMapper(ctx, idp.Alias, m.ID); err != nil {
					return fmt.Errorf("removing mapper %q: %w", m.Name, err)
				}
				u.detail("removed %s", m.Name)
				written++
			}
		}
	}
	if written == 0 {
		u.detail("already as asked; nothing to change")
	}
	return nil
}

// pick returns have's values for the keys of want, to compare what was asked
// for without the keys Keycloak adds of its own.
func pick(have, want map[string]string) map[string]string {
	out := make(map[string]string, len(want))
	for k := range want {
		out[k] = have[k]
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// resolve fills in what was neither asked for nor supplied, and rejects what
// cannot be guessed.
func (idp *IdPOptions) resolve(org *keycloakOrganization, registered bool) error {
	if idp.OrgName == "" {
		idp.OrgName = org.Name
	}
	if idp.OrgDomain == "" {
		idp.OrgDomain = org.Domain()
	}
	if registered && !idp.saml() && idp.DiscoveryURL == "" {
		if idp.OrgDomain == "" {
			return fmt.Errorf("the organization needs an email domain: it is what matches people to this provider")
		}
		return idp.resolveRoles()
	}
	if idp.SAMLMetadataURL != "" && idp.SAMLMetadataFile != "" {
		return fmt.Errorf("--saml-metadata-url and --saml-metadata-file are two ways to give the same thing; pass one")
	}
	if idp.saml() {
		if idp.DiscoveryURL != "" {
			return fmt.Errorf("--discovery-url is for an OpenID Connect provider and --saml-metadata-* for a SAML one; pass one")
		}
		if idp.Alias == "" {
			idp.Alias = aliasFromDiscoveryURL(idp.SAMLMetadataURL)
		}
	} else {
		if idp.DiscoveryURL == "" {
			return fmt.Errorf("the provider's discovery URL or SAML metadata is required: pass --discovery-url, --saml-metadata-url or --saml-metadata-file, or -i to be asked")
		}
		if idp.ClientID == "" || idp.ClientSecret == "" {
			return fmt.Errorf("the provider needs a client id and secret: pass --client-id and --client-secret, or -i to be asked")
		}
		switch idp.ClientAuthMethod {
		case "":
			idp.ClientAuthMethod = "client_secret_post"
		case "client_secret_post", "client_secret_basic":
		default:
			return fmt.Errorf("--client-auth-method %q: use client_secret_post or client_secret_basic", idp.ClientAuthMethod)
		}
		if idp.Alias == "" {
			idp.Alias = aliasFromDiscoveryURL(idp.DiscoveryURL)
		}
	}
	if idp.OrgDomain == "" {
		return fmt.Errorf("the organization needs an email domain: it is what matches people to this provider")
	}
	return idp.resolveRoles()
}

// resolveRoles parses --role-map against the roles the server knows.
func (idp *IdPOptions) resolveRoles() error {
	if len(idp.RoleMap) == 0 {
		return nil
	}
	if idp.RoleAttribute == "" {
		return fmt.Errorf("--role-map needs --role-attribute: the attribute or claim whose values it maps")
	}
	idp.roles = nil
	for _, entry := range idp.RoleMap {
		// The last '=' splits, so a value that is an LDAP DN, full of '=', works.
		i := strings.LastIndex(entry, "=")
		if i <= 0 || i == len(entry)-1 {
			return fmt.Errorf("--role-map %q: expected value=role, such as confighub-admins=admin", entry)
		}
		value, role := entry[:i], strings.ToLower(entry[i+1:])
		if !slices.Contains(orgRoles, role) {
			return fmt.Errorf("--role-map %q: %q is not an org role; use one of %s", entry, role, strings.Join(orgRoles, ", "))
		}
		idp.roles = append(idp.roles, roleMapping{value: value, role: role})
	}
	return nil
}

// interviewIdP asks for what this needs, defaulting to what the instance
// already has.
func interviewIdP(in io.Reader, out io.Writer, org *keycloakOrganization, idp *IdPOptions) error {
	r := bufio.NewReader(in)

	fmt.Fprintf(out, "\nOrganization\n")
	fmt.Fprintf(out, "  The name people will see, and the email domain that decides who\n")
	fmt.Fprintf(out, "  belongs to it.\n\n")

	name, err := ask(r, out, "  name", org.Name)
	if err != nil {
		return err
	}
	idp.OrgName = name

	domain, err := ask(r, out, "  email domain", org.Domain())
	if err != nil {
		return err
	}
	idp.OrgDomain = domain

	if idp.saml() {
		fmt.Fprintln(out)
		return nil
	}

	fmt.Fprintf(out, "\nIdentity provider\n")
	fmt.Fprintf(out, "  Their OpenID discovery document, and the credentials of the\n")
	fmt.Fprintf(out, "  application registered for ConfigHub at their end.\n\n")

	discovery, err := ask(r, out, "  discovery URL", idp.DiscoveryURL)
	if err != nil {
		return err
	}
	idp.DiscoveryURL = discovery

	clientID, err := ask(r, out, "  client id", idp.ClientID)
	if err != nil {
		return err
	}
	idp.ClientID = clientID

	clientSecret, err := askSecret(r, out, "  client secret")
	if err != nil {
		return err
	}
	idp.ClientSecret = clientSecret

	alias := idp.Alias
	if alias == "" {
		alias = aliasFromDiscoveryURL(discovery)
	}
	alias, err = ask(r, out, "  short name (appears in the redirect URI)", alias)
	if err != nil {
		return err
	}
	idp.Alias = alias
	fmt.Fprintln(out)
	return nil
}

// aliasFromDiscoveryURL guesses a short, recognisable name for the provider.
//
// The host says who it is, once the label that only says "this is the login bit"
// is dropped: accounts.google.com is google, login.microsoftonline.com is
// microsoftonline, acme.okta.com is acme. A guess, which is why it is offered as
// a default and not imposed -- it ends up in a redirect URI someone has to
// register, so it should be something they recognise.
func aliasFromDiscoveryURL(discoveryURL string) string {
	const fallback = "sso"

	parsed, err := url.Parse(discoveryURL)
	if err != nil || parsed.Hostname() == "" {
		return fallback
	}
	// A hostname with nothing to distinguish -- localhost, or a bare machine
	// name -- says nothing about who the provider is, and "localhost" in a
	// redirect URI someone has to register elsewhere is actively confusing.
	host := parsed.Hostname()
	if !strings.Contains(host, ".") {
		return fallback
	}
	labels := strings.Split(host, ".")

	generic := map[string]bool{
		"login": true, "accounts": true, "account": true,
		"auth": true, "sso": true, "idp": true, "id": true, "www": true,
	}
	for _, label := range labels {
		if generic[strings.ToLower(label)] {
			continue
		}
		if isPublicSuffixish(label) {
			break
		}
		return sanitize(label)
	}
	return fallback
}

// isPublicSuffixish spots the tail of a hostname without carrying a public
// suffix list around: a guess at a display name does not warrant one, and the
// caller can always type something else.
func isPublicSuffixish(label string) bool {
	switch strings.ToLower(label) {
	case "com", "net", "org", "io", "co", "gov", "edu", "uk", "de", "fr", "eu":
		return true
	}
	return false
}
