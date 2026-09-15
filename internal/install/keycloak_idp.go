package install

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
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

	// DiscoveryURL is the provider's OpenID configuration document. Keycloak
	// reads it and fills in the endpoints, so this replaces naming each one.
	DiscoveryURL string

	ClientID     string
	ClientSecret string

	// Alias names the provider inside Keycloak, and appears in the redirect URI
	// the company has to register at their end.
	Alias string

	// Interactive asks for anything not supplied, starting from what this
	// instance already has.
	Interactive bool
}

// RunIdP sets up the organization and connects a corporate identity provider.
func RunIdP(ctx context.Context, u UI, in io.Reader, o *Options, idp *IdPOptions) error {
	if err := o.Defaults(); err != nil {
		return err
	}
	// The ports this instance is really published on, not the defaults. Only the
	// closing message uses them here, but a message that names an address nobody
	// is listening on is worse than no message.
	if err := o.resolvePorts(); err != nil {
		return err
	}

	u.step("Connecting to Keycloak")
	admin, err := newKeycloakAdmin(ctx, o)
	if err != nil {
		return err
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
	if err := idp.resolve(org); err != nil {
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

	u.step("Reading the provider's configuration")
	exists, err := admin.idpExists(ctx, idp.Alias)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf(
			"realm %q already has an identity provider called %q.\n"+
				"    Pass --alias to add another under a different name, or remove that one in the Keycloak console:\n"+
				"      cub server keycloak open%s",
			admin.realm, idp.Alias, outDirFlag(o))
	}

	config, err := admin.importIdPConfig(ctx, idp.DiscoveryURL)
	if err != nil {
		return err
	}
	u.detail("issuer %s", config["issuer"])

	config["clientId"] = idp.ClientID
	config["clientSecret"] = idp.ClientSecret
	// What makes a person who signs in here a member of the organization: the
	// domain their address is matched against, and being sent to this provider
	// when it matches rather than being shown a password form they have no
	// password for.
	config["kc.org.domain"] = idp.OrgDomain
	config["kc.org.broker.redirect.mode.email-matches"] = "true"

	u.step("Registering it")
	if err := admin.createIdP(ctx, idp.Alias, idp.OrgName, config); err != nil {
		return fmt.Errorf("creating the identity provider: %w", err)
	}
	if err := admin.linkIdPToOrg(ctx, org.ID, idp.Alias); err != nil {
		return fmt.Errorf("linking %q to the organization: %w", idp.Alias, err)
	}
	u.detail("linked to %q", idp.OrgName)

	u.section("Identity provider connected.",
		"organization   "+idp.OrgName+"  ("+idp.OrgDomain+")",
		"provider       "+idp.Alias,
		"",
		"One thing is still owed at their end. Register this redirect URI with the",
		"identity provider, or sign-in will fail on the way back:",
		"",
		"  "+admin.RedirectURI(idp.Alias),
		"",
		"Then anyone with an @"+idp.OrgDomain+" address can sign in at:",
		"",
		"  "+o.APIURL(),
		"",
		"They are made members of "+idp.OrgName+" on the way through, which is what",
		"ConfigHub needs: a user who belongs to no organization is told their",
		"account is pending approval instead of being signed in.",
	)
	return nil
}

// resolve fills in what was neither asked for nor supplied, and rejects what
// cannot be guessed.
func (idp *IdPOptions) resolve(org *keycloakOrganization) error {
	if idp.OrgName == "" {
		idp.OrgName = org.Name
	}
	if idp.OrgDomain == "" {
		idp.OrgDomain = org.Domain()
	}
	if idp.DiscoveryURL == "" {
		return fmt.Errorf("the provider's discovery URL is required: pass --discovery-url, or -i to be asked")
	}
	if idp.ClientID == "" || idp.ClientSecret == "" {
		return fmt.Errorf("the provider needs a client id and secret: pass --client-id and --client-secret, or -i to be asked")
	}
	if idp.Alias == "" {
		idp.Alias = aliasFromDiscoveryURL(idp.DiscoveryURL)
	}
	if idp.OrgDomain == "" {
		return fmt.Errorf("the organization needs an email domain: it is what matches people to this provider")
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
