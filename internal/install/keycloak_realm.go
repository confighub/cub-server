package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"slices"
)

// Bringing a realm to the shape ConfigHub needs, through the admin API.
//
// The bundled Keycloak gets its realm from an import on first start. A Keycloak
// the customer runs has no such moment: the realm may not exist yet, or may
// exist because someone made it by hand, or because this command ran before.
// So each piece here is a reconciliation -- create what is missing, set what
// differs, leave everything else alone -- and the document it reconciles
// against is the same realm the bundled install imports. One description of
// the realm, two ways of arriving at it.

// realmExists reports whether the realm is there at all.
func (k *keycloakAdmin) realmExists(ctx context.Context) (bool, error) {
	return k.exists(ctx, "")
}

// createRealm creates the realm from a whole realm document, the way an import
// would. Keycloak's create-realm endpoint runs the same importer, so clients,
// the organization and the service account's roles arrive in one call.
func (k *keycloakAdmin) createRealm(ctx context.Context, realm map[string]any) error {
	return k.doAt(ctx, http.MethodPost, "/admin/realms", realm, nil)
}

// enableOrganizations turns on the organizations feature for a realm that was
// created without it. ConfigHub resolves a user's organization from the token,
// so a realm without organizations has no way to say which one anyone is in.
func (k *keycloakAdmin) enableOrganizations(ctx context.Context) (bool, error) {
	var realm struct {
		OrganizationsEnabled bool `json:"organizationsEnabled"`
	}
	if err := k.do(ctx, http.MethodGet, "", nil, &realm); err != nil {
		return false, err
	}
	if realm.OrganizationsEnabled {
		return false, nil
	}
	return true, k.do(ctx, http.MethodPut, "", map[string]any{"organizationsEnabled": true}, nil)
}

// clientByID returns the client with this clientId, or nil.
func (k *keycloakAdmin) clientByID(ctx context.Context, clientID string) (map[string]any, error) {
	var clients []map[string]any
	if err := k.do(ctx, http.MethodGet, "/clients?clientId="+url.QueryEscape(clientID), nil, &clients); err != nil {
		return nil, err
	}
	for _, c := range clients {
		if c["clientId"] == clientID {
			return c, nil
		}
	}
	return nil, nil
}

// reconcileClient makes the realm's client match want.
//
// Every field want names is set; fields it does not name are kept, so someone
// who adjusted a token lifetime in the console does not lose it on a re-run.
// Attributes merge key by key. Redirect URIs and web origins are added to
// rather than replaced, because a second address for the UI is a reasonable
// thing to have registered by hand. Protocol mappers are matched by name.
//
// It reports whether anything was written.
func (k *keycloakAdmin) reconcileClient(ctx context.Context, want map[string]any) (bool, error) {
	clientID, _ := want["clientId"].(string)
	have, err := k.clientByID(ctx, clientID)
	if err != nil {
		return false, err
	}
	if have == nil {
		if err := k.do(ctx, http.MethodPost, "/clients", want, nil); err != nil {
			return false, fmt.Errorf("creating client %q: %w", clientID, err)
		}
		return true, nil
	}

	id, _ := have["id"].(string)
	merged := make(map[string]any, len(have))
	for key, value := range have {
		merged[key] = value
	}
	for key, value := range want {
		switch key {
		case "protocolMappers":
			// Separately, below: the client update endpoint ignores them.
		case "attributes":
			// A copy: have is what the comparison below reads.
			attrs, _ := merged["attributes"].(map[string]any)
			attrs = shallowCopy(attrs)
			for name, v := range value.(map[string]any) {
				attrs[name] = v
			}
			merged["attributes"] = attrs
		case "redirectUris", "webOrigins":
			merged[key] = union(merged[key], value)
		default:
			merged[key] = value
		}
	}
	delete(merged, "protocolMappers")

	changed := false
	if !subsetEqual(merged, have, "protocolMappers") {
		if err := k.do(ctx, http.MethodPut, "/clients/"+id, merged, nil); err != nil {
			return false, fmt.Errorf("updating client %q: %w", clientID, err)
		}
		changed = true
	}

	mappersChanged, err := k.reconcileClientMappers(ctx, id, have, want)
	if err != nil {
		return false, fmt.Errorf("client %q: %w", clientID, err)
	}
	return changed || mappersChanged, nil
}

// reconcileClientMappers adds want's protocol mappers to the client, or brings
// one of the same name back to want's configuration.
func (k *keycloakAdmin) reconcileClientMappers(ctx context.Context, id string, have, want map[string]any) (bool, error) {
	wantMappers, _ := want["protocolMappers"].([]any)
	if len(wantMappers) == 0 {
		return false, nil
	}
	existing := map[string]map[string]any{}
	if list, ok := have["protocolMappers"].([]any); ok {
		for _, m := range list {
			if mapper, ok := m.(map[string]any); ok {
				name, _ := mapper["name"].(string)
				existing[name] = mapper
			}
		}
	}

	changed := false
	for _, m := range wantMappers {
		mapper := m.(map[string]any)
		name, _ := mapper["name"].(string)
		current, ok := existing[name]
		if !ok {
			if err := k.do(ctx, http.MethodPost, "/clients/"+id+"/protocol-mappers/models", mapper, nil); err != nil {
				return false, fmt.Errorf("adding mapper %q: %w", name, err)
			}
			changed = true
			continue
		}
		if subsetEqual(mapper, current) {
			continue
		}
		update := shallowCopy(mapper)
		update["id"] = current["id"]
		if err := k.do(ctx, http.MethodPut, fmt.Sprintf("/clients/%s/protocol-mappers/models/%s", id, current["id"]), update, nil); err != nil {
			return false, fmt.Errorf("updating mapper %q: %w", name, err)
		}
		changed = true
	}
	return changed, nil
}

// grantClientRoles gives a client's service account roles of another client,
// such as realm-management's realm-admin. Granting a role it already holds is
// a no-op in Keycloak, so this does not check first.
func (k *keycloakAdmin) grantClientRoles(ctx context.Context, serviceAccountClientID, roleClientID string, roles []string) error {
	owner, err := k.clientByID(ctx, serviceAccountClientID)
	if err != nil {
		return err
	}
	if owner == nil {
		return fmt.Errorf("no client %q to grant roles to", serviceAccountClientID)
	}
	var user struct {
		ID string `json:"id"`
	}
	if err := k.do(ctx, http.MethodGet, fmt.Sprintf("/clients/%s/service-account-user", owner["id"]), nil, &user); err != nil {
		return fmt.Errorf("finding the service account of %q: %w", serviceAccountClientID, err)
	}

	roleClient, err := k.clientByID(ctx, roleClientID)
	if err != nil {
		return err
	}
	if roleClient == nil {
		return fmt.Errorf("no client %q in realm %q", roleClientID, k.realm)
	}
	reps := make([]any, 0, len(roles))
	for _, role := range roles {
		var rep map[string]any
		if err := k.do(ctx, http.MethodGet, fmt.Sprintf("/clients/%s/roles/%s", roleClient["id"], url.PathEscape(role)), nil, &rep); err != nil {
			return fmt.Errorf("finding role %s/%s: %w", roleClientID, role, err)
		}
		reps = append(reps, rep)
	}
	return k.do(ctx, http.MethodPost, fmt.Sprintf("/users/%s/role-mappings/clients/%s", user.ID, roleClient["id"]), reps, nil)
}

// ensureOrganization returns the realm's organization, creating it when the
// realm has none.
//
// A realm with one organization keeps it whatever it is called: renaming is
// `keycloak idp`'s business, and an organization people are already members of
// is not something a re-run should replace. More than one is a shape this
// installer did not create, so it is picked by alias or refused.
func (k *keycloakAdmin) ensureOrganization(ctx context.Context, name, domain string) (*keycloakOrganization, bool, error) {
	var orgs []keycloakOrganization
	if err := k.do(ctx, http.MethodGet, "/organizations", nil, &orgs); err != nil {
		return nil, false, err
	}
	switch {
	case len(orgs) == 1:
		return &orgs[0], false, nil
	case len(orgs) > 1:
		for i := range orgs {
			if orgs[i].Alias == name {
				return &orgs[i], false, nil
			}
		}
		return nil, false, fmt.Errorf("realm %q has %d organizations and none has the alias %q", k.realm, len(orgs), name)
	}

	body := map[string]any{
		"name":    name,
		"alias":   name,
		"enabled": true,
		"domains": []any{map[string]any{"name": domain, "verified": false}},
	}
	if err := k.do(ctx, http.MethodPost, "/organizations", body, nil); err != nil {
		return nil, false, fmt.Errorf("creating organization %q: %w", name, err)
	}
	org, err := k.organization(ctx)
	return org, true, err
}

// orgRoles are the realm roles the server reads a user's org role from, most
// privileged first. The server takes the most privileged one a token carries
// and gives creator to a token that carries none.
var orgRoles = []string{"admin", "manager", "editor", "user", "viewer", "creator", "none"}

// ensureRealmRoles creates the org roles the realm is missing, and returns the
// names it created.
func (k *keycloakAdmin) ensureRealmRoles(ctx context.Context) ([]string, error) {
	var roles []struct {
		Name string `json:"name"`
	}
	if err := k.do(ctx, http.MethodGet, "/roles?briefRepresentation=true&max=1000", nil, &roles); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range roles {
		have[r.Name] = true
	}
	var created []string
	for _, role := range orgRoles {
		if have[role] {
			continue
		}
		body := map[string]any{
			"name":        role,
			"description": "ConfigHub org role. A user holding several gets the most privileged; one holding none gets creator.",
		}
		if err := k.do(ctx, http.MethodPost, "/roles", body, nil); err != nil {
			return created, fmt.Errorf("creating realm role %q: %w", role, err)
		}
		created = append(created, role)
	}
	return created, nil
}

// union adds the strings in add to those in have, keeping have's order.
func union(have, add any) []any {
	out := []any{}
	seen := map[string]bool{}
	for _, list := range []any{have, add} {
		items, _ := list.([]any)
		for _, item := range items {
			s, _ := item.(string)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// shallowCopy copies a map one level deep, so an update can be built without changing
// what was read.
func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// subsetEqual reports whether every key of want, other than those skipped, has
// the same value in have, recursing into nested maps. Keycloak fills in keys of
// its own on what it stores -- mapper config gains claims nobody asked for -- so
// a whole-object comparison would see a change on every run.
func subsetEqual(want, have map[string]any, skip ...string) bool {
	for key, value := range want {
		if slices.Contains(skip, key) {
			continue
		}
		if nested, ok := value.(map[string]any); ok {
			other, _ := have[key].(map[string]any)
			if !subsetEqual(nested, other) {
				return false
			}
			continue
		}
		if !reflect.DeepEqual(roundTrip(value), roundTrip(have[key])) {
			return false
		}
	}
	return true
}

// roundTrip normalizes a value through JSON, so that a []string written here and
// the []any read back from Keycloak compare equal.
func roundTrip(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// realmClients and realmServiceAccounts pick the parts of a rendered realm
// document that reconciliation applies one at a time.
func realmClients(realm map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := realm["clients"].([]any)
	for _, c := range list {
		if client, ok := c.(map[string]any); ok {
			out = append(out, client)
		}
	}
	return out
}

// realmServiceAccountGrants lists, for each service account in the document,
// the client roles it is to hold: service account client -> role client ->
// roles.
func realmServiceAccountGrants(realm map[string]any) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	users, _ := realm["users"].([]any)
	for _, u := range users {
		user, _ := u.(map[string]any)
		owner, _ := user["serviceAccountClientId"].(string)
		if owner == "" {
			continue
		}
		clientRoles, _ := user["clientRoles"].(map[string]any)
		for roleClient, roles := range clientRoles {
			list, _ := roles.([]any)
			for _, r := range list {
				if name, ok := r.(string); ok {
					if out[owner] == nil {
						out[owner] = map[string][]string{}
					}
					if !slices.Contains(out[owner][roleClient], name) {
						out[owner][roleClient] = append(out[owner][roleClient], name)
					}
				}
			}
		}
	}
	return out
}
