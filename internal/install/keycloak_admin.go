package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/confighub/cub-server/internal/config"
)

// Configuring Keycloak after it is installed.
//
// The realm import can only describe a realm that does not exist yet: Keycloak
// reads it on a first start and never again, so anything decided later -- the
// organization's real name, a corporate identity provider -- has to go through
// the admin API.
//
// The credential is the admin password cub-server generated for Keycloak and
// keeps in Keycloak's own Secret. That is the installer acting as the operator,
// which is where this work belongs: the ConfigHub server never sees this
// password and gains no ability to configure its own identity provider.
//
// Admin tokens are short-lived -- around a minute -- and an interactive flow can
// easily take longer than that between reading and writing, so the token is
// re-fetched rather than held. That is a fact learned the hard way: a flow that
// authenticates once and then prompts will fail on the write with a 401 that
// reads like a permissions problem.

// keycloakAdmin talks to Keycloak's admin API as its bootstrap administrator.
type keycloakAdmin struct {
	base     string // browser-facing base URL, which is also what this dials
	realm    string
	user     string
	password string

	client *http.Client

	token   string
	fetched time.Time
}

// tokenLifetime is how long a fetched token is reused before another is asked
// for. Comfortably shorter than Keycloak's own expiry, so a slow call cannot
// land on a token that expired in flight.
const tokenLifetime = 30 * time.Second

// newKeycloakAdmin resolves everything needed to administer this instance's
// Keycloak: where it is, which realm, and the password to get in with.
func newKeycloakAdmin(ctx context.Context, o *Options) (*keycloakAdmin, error) {
	if err := requireKeycloakInstalled(o); err != nil {
		return nil, err
	}

	base, err := keycloakBaseURL(o)
	if err != nil {
		return nil, err
	}
	realm, err := keycloakRealmName(o)
	if err != nil {
		return nil, err
	}
	password, err := keycloakAdminPassword(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("the Keycloak admin password: %w", err)
	}

	// The username is the constant the StatefulSet was rendered with. Nothing
	// exposes it as a flag, so there is no install where it is something else.
	admin := &keycloakAdmin{
		base:     base,
		realm:    realm,
		user:     config.DefaultKeycloakAdminUser,
		password: password,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
	// Fail here rather than at the first write, so a wrong password is reported
	// before the caller has answered any questions.
	if _, err := admin.accessToken(ctx); err != nil {
		return nil, err
	}
	return admin, nil
}

// accessToken returns a usable admin token, fetching a new one when the last is
// older than tokenLifetime.
func (k *keycloakAdmin) accessToken(ctx context.Context) (string, error) {
	if k.token != "" && time.Since(k.fetched) < tokenLifetime {
		return k.token, nil
	}

	// The master realm: the bootstrap administrator lives there, which is also
	// why it can administer the realm this instance uses.
	endpoint := k.base + "/realms/master/protocol/openid-connect/token"
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {k.user},
		"password":   {k.password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching Keycloak at %s: %w", k.base, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Keycloak refused the admin password (%d): %s", resp.StatusCode, truncateBody(body))
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("no access token in Keycloak's response")
	}
	k.token, k.fetched = out.AccessToken, time.Now()
	return k.token, nil
}

// do performs one admin API call against this instance's realm.
//
// path is relative to the realm, so callers read as the thing they are doing
// rather than as URL assembly.
func (k *keycloakAdmin) do(ctx context.Context, method, path string, body, into any) error {
	token, err := k.accessToken(ctx)
	if err != nil {
		return err
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}

	endpoint := k.base + "/admin/realms/" + k.realm + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, truncateBody(raw))
	}
	if into != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, into); err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return nil
}

// keycloakOrganization is the organization ConfigHub resolves a user's
// membership against.
type keycloakOrganization struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Alias   string `json:"alias"`
	Enabled bool   `json:"enabled"`
	Domains []struct {
		Name     string `json:"name"`
		Verified bool   `json:"verified"`
	} `json:"domains"`
}

// Domain is the organization's first email domain, which is the one an identity
// provider is matched against.
func (o keycloakOrganization) Domain() string {
	if len(o.Domains) == 0 {
		return ""
	}
	return o.Domains[0].Name
}

// organization returns the realm's organization.
//
// The realm import creates exactly one, and ConfigHub resolves a user's
// organization from their token, so more than one is a shape this installer did
// not create and should not silently pick from.
func (k *keycloakAdmin) organization(ctx context.Context) (*keycloakOrganization, error) {
	var orgs []keycloakOrganization
	if err := k.do(ctx, http.MethodGet, "/organizations", nil, &orgs); err != nil {
		return nil, err
	}
	switch len(orgs) {
	case 0:
		return nil, fmt.Errorf("realm %q has no organization; a user who belongs to none is told their account is pending approval", k.realm)
	case 1:
		return &orgs[0], nil
	default:
		names := make([]string, 0, len(orgs))
		for _, o := range orgs {
			names = append(names, o.Name)
		}
		return nil, fmt.Errorf("realm %q has %d organizations (%s); this command manages the single one an install creates",
			k.realm, len(orgs), strings.Join(names, ", "))
	}
}

// updateOrganization renames the organization and sets the email domain
// identity providers are matched against.
//
// The alias is left alone: it is how the organization is referred to elsewhere
// in the realm, and Keycloak fixes it at creation.
func (k *keycloakAdmin) updateOrganization(ctx context.Context, org *keycloakOrganization, name, domain string) error {
	body := map[string]any{
		"id":      org.ID,
		"name":    name,
		"alias":   org.Alias,
		"enabled": true,
		"domains": []any{map[string]any{"name": domain, "verified": false}},
	}
	return k.do(ctx, http.MethodPut, "/organizations/"+org.ID, body, nil)
}

// importIdPConfig asks Keycloak to read an OpenID discovery document and return
// the provider configuration described by it.
//
// Keycloak fetches the URL itself, from inside the cluster. That is worth
// knowing when it fails: the address has to be reachable from Keycloak, not from
// the machine running this command.
func (k *keycloakAdmin) importIdPConfig(ctx context.Context, discoveryURL string) (map[string]string, error) {
	var config map[string]string
	body := map[string]string{"fromUrl": discoveryURL, "providerId": "oidc"}
	if err := k.do(ctx, http.MethodPost, "/identity-provider/import-config", body, &config); err != nil {
		return nil, fmt.Errorf("reading the discovery document at %s: %w", discoveryURL, err)
	}
	return config, nil
}

// createIdP registers the identity provider.
//
// The endpoint is /identity-provider/instances -- singular, unlike the
// organization's /identity-providers, which is the sort of thing that costs an
// afternoon if it is not written down.
func (k *keycloakAdmin) createIdP(ctx context.Context, alias, displayName string, config map[string]string) error {
	body := map[string]any{
		"alias":       alias,
		"displayName": displayName,
		"providerId":  "oidc",
		"enabled":     true,
		"config":      config,
	}
	return k.do(ctx, http.MethodPost, "/identity-provider/instances", body, nil)
}

// idpExists reports whether an identity provider of this alias is already there,
// so the flow can say so rather than fail with a conflict.
func (k *keycloakAdmin) idpExists(ctx context.Context, alias string) (bool, error) {
	err := k.do(ctx, http.MethodGet, "/identity-provider/instances/"+alias, nil, nil)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "404") {
		return false, nil
	}
	return false, err
}

// linkIdPToOrg attaches the identity provider to the organization, which is what
// makes someone who signs in through it a member.
//
// The body is the bare alias as a JSON string, which is unusual enough to note.
func (k *keycloakAdmin) linkIdPToOrg(ctx context.Context, orgID, alias string) error {
	return k.do(ctx, http.MethodPost, "/organizations/"+orgID+"/identity-providers", alias, nil)
}

// RedirectURI is the address the corporate identity provider has to be told to
// send people back to.
//
// Built from the browser-facing URL, because that is who follows it.
func (k *keycloakAdmin) RedirectURI(alias string) string {
	return fmt.Sprintf("%s/realms/%s/broker/%s/endpoint", k.base, k.realm, alias)
}

// truncateBody keeps an error readable: Keycloak answers some failures with a
// whole HTML page.
func truncateBody(body []byte) string {
	const max = 300
	s := strings.TrimSpace(string(body))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
