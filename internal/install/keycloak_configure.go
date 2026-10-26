package install

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// Setting up a Keycloak that someone else runs.
//
// `keycloak install` deploys Keycloak beside an instance this plugin installed,
// and so can import a realm on first start and apply the server's new
// configuration itself. Neither is true for an instance deployed from manifests
// someone edited and applied on their own, with a Keycloak they run themselves:
// there is no record to re-render, the cluster may refuse what this plugin would
// apply, and the realm is created whenever this happens to run.
//
// So this does the two halves separately. Keycloak is set up through its admin
// API, from the same realm document the bundled install imports. The server's
// half is written out as patches for the caller to merge into their manifests
// or apply to what is running. Nothing here touches a cluster.
//
// It is safe to re-run. The realm is reconciled rather than replaced, and the
// server's key is read back from the patch written last time. If that patch is
// gone a new key is made and Keycloak is told about it, which is a rotation:
// the server needs the new Secret before it can reach Keycloak again.

// ConfigureOptions is what `cub server keycloak configure` needs.
type ConfigureOptions struct {
	// Keycloak is the realm and its clients. PublicURL is where browsers reach
	// Keycloak; it is the issuer in every token.
	Keycloak config.Keycloak

	// AdminURL is where this command reaches Keycloak's admin API, when that is
	// not PublicURL (an admin hostname, or a port-forward).
	AdminURL string
	Admin    KeycloakAdminCredential

	// UIURL is where browsers reach the ConfigHub UI. Keycloak sends people back
	// there, and only there.
	UIURL string

	// InternalURL is where the server reaches Keycloak, when that is not
	// PublicURL.
	InternalURL string

	// Namespace is where the instance runs, for the commands this prints.
	Namespace string

	// OutDir is where the server's patches are written.
	OutDir string
}

// The three objects the server's configuration lives in, named as the
// manifests from `cub server install` name them.
const (
	serverConfigMap = "confighub-env"
	serverSecret    = "confighub-secret-env"
	uiConfigMap     = "confighub-ui-env"
)

// RunConfigure sets up a realm for ConfigHub on a Keycloak the caller runs.
func RunConfigure(ctx context.Context, u UI, in io.Reader, c *ConfigureOptions) error {
	if err := c.defaults(); err != nil {
		return err
	}
	if err := c.resolveCredential(in, u.Out); err != nil {
		return err
	}
	k := &c.Keycloak

	clientKey, rotated, err := c.clientKey()
	if err != nil {
		return err
	}
	clientJWKS, err := config.ClientJWKS(clientKey)
	if err != nil {
		return err
	}
	doc, err := config.RenderRealm(k, clientJWKS)
	if err != nil {
		return err
	}
	var realm map[string]any
	if err := json.Unmarshal(doc, &realm); err != nil {
		return err
	}

	u.step("Connecting to Keycloak")
	admin, err := newKeycloakAdminAt(ctx, c.AdminURL, k.Realm, c.Admin)
	if err != nil {
		return err
	}
	u.detail("%s", c.AdminURL)

	u.step("Setting up realm %q", k.Realm)
	exists, err := admin.realmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if err := admin.createRealm(ctx, realm); err != nil {
			return fmt.Errorf("creating realm %q: %w", k.Realm, err)
		}
		u.detail("created")
	} else {
		u.detail("exists; bringing it to the shape ConfigHub needs")
		enabled, err := admin.enableOrganizations(ctx)
		if err != nil {
			return fmt.Errorf("enabling organizations: %w", err)
		}
		if enabled {
			u.detail("turned on organizations")
		}
	}

	// On a realm just created these find everything in place. They run anyway,
	// so the two paths end in the same state by the same checks.
	for _, client := range realmClients(realm) {
		changed, err := admin.reconcileClient(ctx, client)
		if err != nil {
			return err
		}
		if changed {
			u.detail("client %s: updated", client["clientId"])
		}
	}
	for owner, grants := range realmServiceAccountGrants(realm) {
		for roleClient, roles := range grants {
			if err := admin.grantClientRoles(ctx, owner, roleClient, roles); err != nil {
				return fmt.Errorf("granting %s %s roles: %w", owner, roleClient, err)
			}
		}
	}
	org, created, err := admin.ensureOrganization(ctx, k.Org, k.OrgDomain)
	if err != nil {
		return err
	}
	if created {
		u.detail("organization %q created, email domain %s", org.Name, org.Domain())
	} else {
		u.detail("organization %q, email domain %s", org.Name, org.Domain())
	}
	if err := admin.enableOrganizationIDClaim(ctx); err != nil {
		return fmt.Errorf("configuring the organization claim: %w", err)
	}
	roles, err := admin.ensureRealmRoles(ctx)
	if err != nil {
		return err
	}
	if len(roles) > 0 {
		u.detail("org roles created: %s", strings.Join(roles, ", "))
	}

	// Written before the key is checked, not after: Keycloak already holds its
	// public half, so a failed check has to leave the private half where a
	// re-run will find it.
	paths, err := c.writePatches(clientKey)
	if err != nil {
		return err
	}

	// The server's credential, tried the way the server will use it. A key
	// Keycloak does not accept fails here rather than as a server that will
	// not start.
	u.step("Checking the server's key")
	if err := admin.checkClientKey(ctx, k.PublicURL, k.ClientID, clientKey); err != nil {
		return err
	}
	u.detail("Keycloak accepts an assertion signed with it")

	return c.report(u, paths, rotated)
}

func (c *ConfigureOptions) defaults() error {
	k := &c.Keycloak
	if k.PublicURL == "" {
		return fmt.Errorf("--keycloak-url is required: where browsers reach Keycloak, and so the issuer of every token")
	}
	if c.UIURL == "" {
		return fmt.Errorf("--ui-url is required: where browsers reach the ConfigHub UI, which is where Keycloak sends them back")
	}
	for name, raw := range map[string]string{"--keycloak-url": k.PublicURL, "--ui-url": c.UIURL, "--admin-url": c.AdminURL, "--internal-url": c.InternalURL} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%s %q is not an http(s) URL", name, raw)
		}
	}
	k.PublicURL = strings.TrimSuffix(k.PublicURL, "/")
	c.UIURL = strings.TrimSuffix(c.UIURL, "/")
	// The trailing slash is what the UI sends as redirect_uri, and OAuth
	// matches redirect URIs exactly.
	k.UIRedirectURI = c.UIURL + "/"
	// A Keycloak serving real users is behind TLS, and a realm that allows
	// plain http from outside is one that will hand tokens over it.
	if k.SSLRequired == "" {
		k.SSLRequired = "external"
	}
	k.Defaults()
	if c.AdminURL == "" {
		c.AdminURL = k.PublicURL
	}
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace
	}
	if c.OutDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine a home directory for the generated config: %w", err)
		}
		host, _ := url.Parse(k.PublicURL)
		c.OutDir = filepath.Join(home, ".confighub", "keycloak", sanitize(host.Host), k.Realm)
	}
	return nil
}

// Environment variables an admin secret can be passed in, so it stays out of
// shell history and process listings.
const (
	adminPasswordEnv     = "KEYCLOAK_ADMIN_PASSWORD"
	adminClientSecretEnv = "KEYCLOAK_ADMIN_CLIENT_SECRET"
)

// resolveCredential settles which admin credential to use, asking for the
// password when it is the only thing missing.
func (c *ConfigureOptions) resolveCredential(in io.Reader, out io.Writer) error {
	return resolveAdminCredential(&c.Admin, in, out)
}

func resolveAdminCredential(cred *KeycloakAdminCredential, in io.Reader, out io.Writer) error {
	if cred.ClientID != "" {
		if cred.User != "" {
			return fmt.Errorf("--admin-user and --admin-client-id are two ways to sign in; pass one")
		}
		if cred.ClientSecret == "" {
			cred.ClientSecret = os.Getenv(adminClientSecretEnv)
		}
		if cred.ClientSecret == "" {
			return fmt.Errorf("the admin client's secret goes in $%s", adminClientSecretEnv)
		}
		return nil
	}
	if cred.User == "" {
		cred.User = "admin"
	}
	if cred.Password == "" {
		cred.Password = os.Getenv(adminPasswordEnv)
	}
	if cred.Password == "" {
		password, err := askSecret(bufio.NewReader(in), out, fmt.Sprintf("Keycloak password for %s (or set $%s)", cred.User, adminPasswordEnv))
		if err != nil {
			return err
		}
		if password == "" {
			return fmt.Errorf("no admin password")
		}
		cred.Password = password
	}
	return nil
}

func (c *ConfigureOptions) secretPatchPath() string {
	return filepath.Join(c.OutDir, serverSecret+".patch.yaml")
}

// clientKey reads back the key the last run wrote, or makes one. rotated says a
// new key replaced one Keycloak already held for this client.
func (c *ConfigureOptions) clientKey() (key string, rotated bool, err error) {
	content, err := os.ReadFile(c.secretPatchPath())
	switch {
	case err == nil:
		var patch struct {
			StringData map[string]string `yaml:"stringData"`
		}
		if err := yaml.Unmarshal(content, &patch); err == nil && patch.StringData[config.KeycloakClientKeyEnv] != "" {
			return patch.StringData[config.KeycloakClientKeyEnv], false, nil
		}
		return "", false, fmt.Errorf("%s has no %s; move it aside to make a new key", c.secretPatchPath(), config.KeycloakClientKeyEnv)
	case os.IsNotExist(err):
		key, err := config.GenerateClientKey()
		return key, true, err
	default:
		return "", false, err
	}
}

// writePatches writes the server's and the UI's configuration as merge patches
// on the objects the generated manifests name. Each is also a plain list of
// what to add, for whoever keeps their manifests by hand.
func (c *ConfigureOptions) writePatches(clientKey string) (map[string]string, error) {
	if err := os.MkdirAll(c.OutDir, 0o700); err != nil {
		return nil, err
	}
	groups := map[config.Placement][]config.Var{}
	for _, v := range c.Keycloak.Vars(c.InternalURL, clientKey) {
		if v.Value == "" {
			continue
		}
		groups[v.Placement] = append(groups[v.Placement], v)
	}

	paths := map[string]string{}
	for _, target := range []struct {
		placement config.Placement
		object    string
		field     string
		mode      os.FileMode
	}{
		{config.InConfigMap, serverConfigMap, "data", 0o644},
		{config.InSecret, serverSecret, "stringData", 0o600},
		{config.InUIConfigMap, uiConfigMap, "data", 0o644},
	} {
		var b strings.Builder
		fmt.Fprintf(&b, "# Merge into %s, or: kubectl patch ... --type merge --patch-file <this file>\n", target.object)
		fmt.Fprintf(&b, "# Written by `cub server keycloak configure`.\n")
		fmt.Fprintf(&b, "%s:\n", target.field)
		for _, v := range groups[target.placement] {
			quoted, _ := json.Marshal(v.Value)
			fmt.Fprintf(&b, "  # %s\n  %s: %s\n", v.Doc, v.Name, quoted)
		}
		path := filepath.Join(c.OutDir, target.object+".patch.yaml")
		if err := os.WriteFile(path, []byte(b.String()), target.mode); err != nil {
			return nil, err
		}
		// WriteFile keeps the mode of a file that already exists.
		if err := os.Chmod(path, target.mode); err != nil {
			return nil, err
		}
		paths[target.object] = path
	}
	return paths, nil
}

func (c *ConfigureOptions) report(u UI, paths map[string]string, rotated bool) error {
	k := c.Keycloak
	ns := c.Namespace
	lines := []string{
		"realm        " + k.Realm,
		"issuer       " + k.Issuer(),
		"UI client    " + k.UIClientID + "  (returns to " + k.UIRedirectURI + ")",
		"",
		"Point the server and the UI at it. Merge these into the manifests you",
		"apply, so the next apply keeps them:",
		"",
		"  " + paths[serverConfigMap] + "   ConfigMap " + serverConfigMap,
		"  " + paths[serverSecret] + "   Secret " + serverSecret,
		"  " + paths[uiConfigMap] + "   ConfigMap " + uiConfigMap,
		"",
		"or patch what is running:",
		"",
		fmt.Sprintf("  kubectl -n %s patch configmap %s --type merge --patch-file %s", ns, serverConfigMap, paths[serverConfigMap]),
		fmt.Sprintf("  kubectl -n %s patch secret %s --type merge --patch-file %s", ns, serverSecret, paths[serverSecret]),
		fmt.Sprintf("  kubectl -n %s patch configmap %s --type merge --patch-file %s", ns, uiConfigMap, paths[uiConfigMap]),
		fmt.Sprintf("  kubectl -n %s rollout restart deployment/confighub deployment/confighub-ui", ns),
		"",
		"Everyone who signs in has to be a member of the " + k.Org + " organization,",
		"and gets the creator org role until given another. Give the first",
		"administrator the admin realm role in the Keycloak console, under Users,",
		"after their first sign-in, or map it from your identity provider with",
		"`cub server keycloak idp --role-map`.",
	}
	if rotated {
		lines = append(lines, "",
			"A new server key was made. If this realm was set up before, the server",
			"cannot reach Keycloak until it has the new Secret.")
	}
	u.section("Keycloak is set up for ConfigHub.", lines...)
	return nil
}
