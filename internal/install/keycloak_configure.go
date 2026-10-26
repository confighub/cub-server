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
	"slices"
	"strings"

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

	// OutDir is where the server's patches are written, and where the server's
	// key is kept for the next run.
	OutDir string

	// ClientKeyFile is the server's key, when it is kept somewhere other than
	// OutDir -- typically fetched from the secret store it lives in.
	ClientKeyFile string

	// Secrets is how the server's Secret is produced: SecretsKubernetes, applied
	// directly; SecretsExternal, by an ExternalSecret named ExternalSecret; or
	// SecretsStoreCSI, synced by the Secrets Store CSI driver through the
	// SecretProviderClass named SecretProviderClass. The last two read the
	// secret-store entry RemoteKey (and RemoteProperty, when that entry holds
	// several values).
	Secrets             string
	ExternalSecret      string
	SecretProviderClass string
	RemoteKey           string
	RemoteProperty      string
}

const (
	SecretsKubernetes = "kubernetes"
	SecretsExternal   = "external-secrets"
	SecretsStoreCSI   = "secrets-store-csi"
)

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

	clientKey, generated, err := c.clientKey()
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
	out, err := c.writeOutputs(clientKey)
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

	return c.report(u, out, generated)
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
	external := c.RemoteKey != "" || c.RemoteProperty != ""
	switch c.Secrets {
	case "", SecretsKubernetes:
		c.Secrets = SecretsKubernetes
		if external {
			return fmt.Errorf("--remote-key and --remote-property are for --secrets %s or %s", SecretsExternal, SecretsStoreCSI)
		}
	case SecretsExternal, SecretsStoreCSI:
		if c.RemoteKey == "" {
			return fmt.Errorf("--secrets %s needs --remote-key: where in your secret store the server's key is kept", c.Secrets)
		}
	default:
		return fmt.Errorf("--secrets %q: use %s, %s or %s", c.Secrets, SecretsKubernetes, SecretsExternal, SecretsStoreCSI)
	}
	if c.ExternalSecret != "" && c.Secrets != SecretsExternal {
		return fmt.Errorf("--external-secret is for --secrets %s", SecretsExternal)
	}
	if c.SecretProviderClass != "" && c.Secrets != SecretsStoreCSI {
		return fmt.Errorf("--secret-provider-class is for --secrets %s", SecretsStoreCSI)
	}
	if c.Secrets == SecretsExternal && c.ExternalSecret == "" {
		c.ExternalSecret = serverSecret
	}
	// Required rather than guessed: the name ends up in the Deployment's volume,
	// and a wrong one fails as a pod that never starts, far from here.
	if c.Secrets == SecretsStoreCSI && c.SecretProviderClass == "" {
		return fmt.Errorf("--secrets %s needs --secret-provider-class: the SecretProviderClass the server's Secret is synced through", SecretsStoreCSI)
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

// clientKeyFile is where the server's key is kept between runs, on its own, so
// it can also be put into a secret store as it is.
const clientKeyFile = "keycloak-client-key.jwk"

// clientKey returns the server's key: the one passed with --client-key-file,
// else the one the last run kept in --out-dir, else a new one. generated says
// it is new, so Keycloak is about to be switched to it.
func (c *ConfigureOptions) clientKey() (key string, generated bool, err error) {
	path := c.ClientKeyFile
	if path == "" {
		path = filepath.Join(c.OutDir, clientKeyFile)
	}
	content, err := os.ReadFile(path)
	switch {
	case err == nil:
		key := strings.TrimSpace(string(content))
		if _, err := config.ClientJWKS(key); err != nil {
			return "", false, fmt.Errorf("%s does not hold the server's Ed25519 key: %w", path, err)
		}
		return key, false, nil
	case os.IsNotExist(err) && c.ClientKeyFile == "":
		key, err := config.GenerateClientKey()
		return key, true, err
	default:
		return "", false, err
	}
}

// configureOutputs are the files a run writes, by what they are for.
type configureOutputs struct {
	serverConfigMap, uiConfigMap string
	secretPatch                  string // --secrets kubernetes
	externalSecretPatch          string // --secrets external-secrets
	externalSecretEntry          string // the same entry, to merge by hand
	secretProviderClass          string // --secrets secrets-store-csi
	clientKey                    string
}

// writeOutputs writes the server's and the UI's configuration as merge patches
// on the objects the generated manifests name, each also a plain list of what
// to add for whoever keeps their manifests by hand. The server's key goes to a
// Secret patch, or, when an ExternalSecret owns that Secret, to a file for the
// secret store and an entry for the ExternalSecret: a patch on a Secret that
// External Secrets manages lasts until its next sync.
func (c *ConfigureOptions) writeOutputs(clientKey string) (*configureOutputs, error) {
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

	out := &configureOutputs{
		serverConfigMap:     filepath.Join(c.OutDir, serverConfigMap+".patch.yaml"),
		uiConfigMap:         filepath.Join(c.OutDir, uiConfigMap+".patch.yaml"),
		secretPatch:         filepath.Join(c.OutDir, serverSecret+".patch.yaml"),
		externalSecretPatch: filepath.Join(c.OutDir, c.ExternalSecret+".externalsecret-patch.json"),
		externalSecretEntry: filepath.Join(c.OutDir, c.ExternalSecret+".externalsecret-entry.yaml"),
		secretProviderClass: filepath.Join(c.OutDir, c.SecretProviderClass+".secretproviderclass-entries.yaml"),
		clientKey:           filepath.Join(c.OutDir, clientKeyFile),
	}
	if err := writeFile(out.clientKey, []byte(clientKey+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := writeFile(out.serverConfigMap, mergePatch(serverConfigMap, "data", groups[config.InConfigMap]), 0o644); err != nil {
		return nil, err
	}
	if err := writeFile(out.uiConfigMap, mergePatch(uiConfigMap, "data", groups[config.InUIConfigMap]), 0o644); err != nil {
		return nil, err
	}

	// The files of the other modes are removed, so a directory that has been
	// used more than one way does not offer a patch that no longer applies.
	stale := []string{out.secretPatch}
	for _, pattern := range []string{"*.externalsecret-patch.json", "*.externalsecret-entry.yaml", "*.secretproviderclass-entries.yaml"} {
		matches, _ := filepath.Glob(filepath.Join(c.OutDir, pattern))
		stale = append(stale, matches...)
	}
	var keep []string
	switch c.Secrets {
	case SecretsExternal:
		entry, patch := externalSecretEntry(config.KeycloakClientKeyEnv, c.RemoteKey, c.RemoteProperty)
		if err := writeFile(out.externalSecretEntry, entry, 0o644); err != nil {
			return nil, err
		}
		if err := writeFile(out.externalSecretPatch, patch, 0o644); err != nil {
			return nil, err
		}
		keep = []string{out.externalSecretEntry, out.externalSecretPatch}
	case SecretsStoreCSI:
		fragments := secretProviderClassEntries(c.SecretProviderClass, config.KeycloakClientKeyEnv, c.RemoteKey, c.RemoteProperty)
		if err := writeFile(out.secretProviderClass, fragments, 0o644); err != nil {
			return nil, err
		}
		keep = []string{out.secretProviderClass}
	default:
		if err := writeFile(out.secretPatch, mergePatch(serverSecret, "stringData", groups[config.InSecret]), 0o600); err != nil {
			return nil, err
		}
		keep = []string{out.secretPatch}
	}
	stale = slices.DeleteFunc(stale, func(path string) bool { return slices.Contains(keep, path) })
	for _, path := range stale {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}

// writeFile writes content with mode, including over a file that exists with
// another one: WriteFile keeps the mode of a file it does not create.
func writeFile(path string, content []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func mergePatch(object, field string, vars []config.Var) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Merge into %s, or: kubectl patch ... --type merge --patch-file <this file>\n", object)
	fmt.Fprintf(&b, "# Written by `cub server keycloak configure`.\n")
	fmt.Fprintf(&b, "%s:\n", field)
	for _, v := range vars {
		quoted, _ := json.Marshal(v.Value)
		fmt.Fprintf(&b, "  # %s\n  %s: %s\n", v.Doc, v.Name, quoted)
	}
	return []byte(b.String())
}

// externalSecretEntry is the spec.data entry that brings one key from the
// secret store into the Secret, as YAML to merge by hand and as a JSON patch
// that appends it.
func externalSecretEntry(secretKey, remoteKey, property string) (entryYAML, patchJSON []byte) {
	remoteRef := map[string]any{"key": remoteKey}
	if property != "" {
		remoteRef["property"] = property
	}
	entry := map[string]any{"secretKey": secretKey, "remoteRef": remoteRef}

	var b strings.Builder
	b.WriteString("# Add to spec.data of the ExternalSecret that produces the server's Secret.\n")
	b.WriteString("# Written by `cub server keycloak configure`.\n")
	fmt.Fprintf(&b, "- secretKey: %s\n  remoteRef:\n    key: %q\n", secretKey, remoteKey)
	if property != "" {
		fmt.Fprintf(&b, "    property: %q\n", property)
	}

	patch, _ := json.MarshalIndent([]any{map[string]any{"op": "add", "path": "/spec/data/-", "value": entry}}, "", "  ")
	return []byte(b.String()), append(patch, '\n')
}

// secretProviderClassEntries is what to add to a SecretProviderClass (Secrets
// Store CSI driver, AWS provider) so the server's Secret carries one more key
// from AWS Secrets Manager, and the volume without which the driver never
// writes that Secret: it syncs a Secret only while a pod mounts the class.
func secretProviderClassEntries(class, secretKey, remoteKey, property string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# What to add to the SecretProviderClass %q so the server's Secret takes\n", class)
	fmt.Fprintf(&b, "# %s from AWS Secrets Manager.\n", secretKey)
	b.WriteString("# Written by `cub server keycloak configure`.\n")
	b.WriteString("#\n# 1. In spec.parameters.objects. If that secret is listed there already, add\n")
	b.WriteString("#    only its jmesPath entry to the existing one.\n")
	fmt.Fprintf(&b, "- objectName: %q\n  objectType: \"secretsmanager\"\n", remoteKey)
	if property != "" {
		// The driver writes every object as a file named after it, which a
		// name with a slash in it cannot be, so such a name gets an alias.
		if strings.Contains(remoteKey, "/") {
			fmt.Fprintf(&b, "  objectAlias: %q\n", sanitize(remoteKey))
		}
		fmt.Fprintf(&b, "  jmesPath:\n    - path: %q\n      objectAlias: %q\n", property, secretKey)
	} else {
		fmt.Fprintf(&b, "  objectAlias: %q\n", secretKey)
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "# 2. In spec.secretObjects, in the data of the entry with secretName %s\n", serverSecret)
	b.WriteString("#    (add that entry, type Opaque, if there is none).\n")
	fmt.Fprintf(&b, "- objectName: %q\n  key: %q\n", secretKey, secretKey)
	b.WriteString("---\n")
	b.WriteString("# 3. The driver writes the Secret only while a pod mounts this class. If the\n")
	b.WriteString("#    confighub Deployment does not already, add to spec.template.spec.volumes:\n")
	fmt.Fprintf(&b, "- name: secrets-store\n  csi:\n    driver: secrets-store.csi.k8s.io\n    readOnly: true\n    volumeAttributes:\n      secretProviderClass: %q\n", class)
	b.WriteString("---\n")
	b.WriteString("# 4. And to the confighub container's volumeMounts:\n")
	b.WriteString("- name: secrets-store\n  mountPath: /mnt/secrets-store\n  readOnly: true\n")
	return []byte(b.String())
}

func (c *ConfigureOptions) report(u UI, out *configureOutputs, generated bool) error {
	k := c.Keycloak
	ns := c.Namespace
	lines := []string{
		"realm        " + k.Realm,
		"issuer       " + k.Issuer(),
		"UI client    " + k.UIClientID + "  (returns to " + k.UIRedirectURI + ")",
		"server key   " + out.clientKey,
		"",
		"Point the server and the UI at it. Merge these into the manifests you",
		"apply, so the next apply keeps them:",
		"",
		"  " + out.serverConfigMap + "   ConfigMap " + serverConfigMap,
		"  " + out.uiConfigMap + "   ConfigMap " + uiConfigMap,
	}
	where := c.RemoteKey
	if c.RemoteProperty != "" {
		where += ", property " + c.RemoteProperty
	}
	switch c.Secrets {
	case SecretsStoreCSI:
		lines = append(lines,
			"  "+out.secretProviderClass+"   SecretProviderClass "+c.SecretProviderClass+", Deployment confighub",
			"",
			"The server's key goes in AWS Secrets Manager, not in the cluster. Store the",
			"contents of "+out.clientKey,
			"at "+where+". The confighub pod's service account needs read access",
			"to that secret, the same as for anything else this class fetches.",
			"",
			"Then patch the ConfigMaps of what is running:",
			"",
		)
	case SecretsExternal:
		lines = append(lines,
			"  "+out.externalSecretEntry+"   ExternalSecret "+c.ExternalSecret,
			"",
			"The server's key goes in your secret store, not in the cluster. Store the",
			"contents of "+out.clientKey,
			"at "+where+". If the ExternalSecret reads",
			"that secret whole (dataFrom), store it as the "+config.KeycloakClientKeyEnv,
			"property instead and skip the entry above.",
			"",
			"Or patch what is running, once:",
			"",
			fmt.Sprintf("  kubectl -n %s patch externalsecret %s --type json --patch-file %s", ns, c.ExternalSecret, out.externalSecretPatch),
		)
	default:
		lines = append(lines,
			"  "+out.secretPatch+"   Secret "+serverSecret,
			"",
			"or patch what is running:",
			"",
			fmt.Sprintf("  kubectl -n %s patch secret %s --type merge --patch-file %s", ns, serverSecret, out.secretPatch),
		)
	}
	lines = append(lines,
		fmt.Sprintf("  kubectl -n %s patch configmap %s --type merge --patch-file %s", ns, serverConfigMap, out.serverConfigMap),
		fmt.Sprintf("  kubectl -n %s patch configmap %s --type merge --patch-file %s", ns, uiConfigMap, out.uiConfigMap),
		fmt.Sprintf("  kubectl -n %s rollout restart deployment/confighub deployment/confighub-ui", ns),
		"",
		"Everyone who signs in has to be a member of the "+k.Org+" organization,",
		"and gets the creator org role until given another. Give the first",
		"administrator the admin realm role in the Keycloak console, under Users,",
		"after their first sign-in, or map it from your identity provider with",
		"`cub server keycloak idp --role-map`.",
	)
	if generated {
		lines = append(lines, "",
			"A new server key was made. If this realm was set up before, the server",
			"cannot reach Keycloak until it has the new key. Keep "+out.clientKey+",",
			"or pass it back with --client-key-file, so the next run reuses it.")
	}
	u.section("Keycloak is set up for ConfigHub.", lines...)
	return nil
}
