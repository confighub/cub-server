package config

import (
	"fmt"
	"strconv"
)

// Surface is everything the server reads, resolved for one instance.
type Surface struct {
	Vars []Var

	// AdminKey is set only when Build minted one, meaning the caller supplied no
	// public key and the private half now has to reach them. Nil when the caller
	// brought their own, because then there is no private half here to hand back.
	AdminKey *AdminKeypair
}

// ConfigMapVars and SecretVars split the surface by where each value belongs.
// The split is read from the variables themselves; a renderer never decides it.
func (s *Surface) ConfigMapVars() []Var { return s.byPlacement(InConfigMap) }
func (s *Surface) SecretVars() []Var    { return s.byPlacement(InSecret) }

// KeycloakSecretVars are the values only Keycloak reads. See InKeycloakSecret.
func (s *Surface) KeycloakSecretVars() []Var { return s.byPlacement(InKeycloakSecret) }

// UIConfigMapVars are the values the UI container reads. See InUIConfigMap.
func (s *Surface) UIConfigMapVars() []Var { return s.byPlacement(InUIConfigMap) }

func (s *Surface) byPlacement(p Placement) []Var {
	var out []Var
	for _, v := range s.Vars {
		if v.Placement == p && v.Value != "" {
			out = append(out, v)
		}
	}
	return out
}

// Get returns a variable's resolved value, or empty if it is not in the surface.
func (s *Surface) Get(name string) string {
	for _, v := range s.Vars {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

// Preserved is what a previous run generated, keyed by variable name.
//
// Re-running the generator must not rotate anything: a fresh JWT_PRIVATE_KEY_JWK
// invalidates every session on the next restart, and a fresh
// WORKER_MASTER_SECRET breaks every worker already enrolled. Installers re-run,
// so the safe behaviour has to be the default one, not a flag.
type Preserved map[string]string

// Build resolves the surface for an instance.
//
// Every variable here is one the server actually reads. That is the property
// worth protecting, and it is what separates generated configuration from
// maintained configuration.
func Build(opts Options, prior Preserved) (*Surface, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if prior == nil {
		prior = Preserved{}
	}

	s := &Surface{}

	// keep resolves a generated value, reusing the previous one when there is
	// one. generate is only called when there is not.
	keep := func(name string, generate func() (string, error)) (string, bool, error) {
		if existing, ok := prior[name]; ok && existing != "" {
			return existing, false, nil
		}
		v, err := generate()
		if err != nil {
			return "", false, err
		}
		return v, true, nil
	}

	add := func(v Var) { s.Vars = append(s.Vars, v) }

	// --- Non-secret -------------------------------------------------------

	// Nothing is emitted for a value the server already arrives at on its own.
	// A generated file that restates a default is worse than one that omits it:
	// it reads as a decision, so a later change to the default silently does not
	// apply here, and no one can tell which keys were chosen and which were
	// copied.
	//
	// Deliberately absent, each already correct without us:
	//
	//   PORT                  the server listens on 9090 unset
	//   ENABLE_OCI_SERVER     the OCI registry is on unless set to "false"
	//   CONFIGHUB_MIGRATIONS  the image sets it; restating it here just
	//                         duplicates what the image already declares
	//
	// A variable belongs below only if the server cannot work it out: something
	// about this deployment, or a secret that has to be generated.

	// The bootstrap administrator's public key. Not a secret, and deliberately
	// so: ConfigHub stores public keys rather than password verifiers, which is
	// what makes this safe in ordinary configuration.
	adminJWK := opts.AdminPublicJWK
	if adminJWK == "" {
		pair, err := GenerateAdminKeypair()
		if err != nil {
			return nil, err
		}
		adminJWK = pair.PublicJWK
		s.AdminKey = pair
	}
	add(Var{
		Name: AdminJWKEnv, Placement: InConfigMap, Value: adminJWK,
		Doc: "Public key of the bootstrap administrator. Not secret: the private half never reaches the cluster.",
	})

	// Where clients pull images from, when that is not where the registry binds.
	//
	// /api/info advertises the registry so that tools configured from it -- Argo
	// CD among them -- know where to pull. The server derives the port from the
	// scheme when nothing says otherwise, which gives 9092: correct for the
	// container port, wrong for anyone outside the cluster, who reaches it on the
	// NodePort. The two only coincide when the registry is reached directly.
	//
	// The host needs no override here. The server keeps the host the request
	// arrived on for an IP or localhost, rather than prefixing "oci.", because
	// there is nothing for that prefix to resolve against -- which is exactly
	// this deployment.
	if opts.OCINodePort != 0 {
		add(Var{
			Name: "OCI_EXTERNAL_PORT", Placement: InConfigMap,
			Value: strconv.Itoa(opts.OCINodePort),
			Doc:   "Port clients pull images from. The NodePort, not the port the registry binds.",
		})
	}

	// Where the UI sends its API calls. The browser makes them, so this is the
	// address the browser reaches the server on, not a cluster-internal one.
	add(Var{
		Name: "CONFIGHUB_URL", Placement: InUIConfigMap, Value: opts.APIURL,
		Doc: "Where the browser reaches the ConfigHub API. The UI is served from a different origin.",
	})

	// The bundled identity provider, when there is one. Absent entirely on an
	// instance whose only identity is the administrator's key: the server treats
	// a realm, auth URL and redirect URI as all-or-nothing, and half of them is
	// fatal rather than ignored.
	if kc := opts.Keycloak; kc != nil {
		add(Var{
			Name: "KEYCLOAK_REALM", Placement: InConfigMap, Value: kc.Realm,
			Doc: "Realm this instance's users live in.",
		})
		add(Var{
			Name: "KEYCLOAK_AUTH_URL", Placement: InConfigMap, Value: kc.PublicURL,
			Doc: "Where a browser reaches Keycloak, and so the issuer of every token it mints.",
		})
		add(Var{
			Name: "KEYCLOAK_INTERNAL_URL", Placement: InConfigMap, Value: kc.InternalURL(opts.Namespace),
			Doc: "Where the server reaches Keycloak. The address above is the browser's and does not resolve in here.",
		})
		add(Var{
			Name: "KEYCLOAK_CLIENT_ID", Placement: InConfigMap, Value: kc.ClientID,
			Doc: "The client the server authenticates as.",
		})
		add(Var{
			Name: "KEYCLOAK_DEVICE_CLIENT_ID", Placement: InConfigMap, Value: kc.DeviceClientID,
			Doc: "The public client cub authenticates through. Public because it runs on the user's machine.",
		})

		// What lets the UI sign in as its own OAuth client.
		//
		// The three go together and mean one thing: the browser runs OIDC against
		// the issuer, gets a token stamped with the audience, and exchanges it at
		// the server for a ConfigHub one. The client id is the UI's to know, so
		// it goes to the UI container; the issuer and audience are what the
		// server checks, so they go to the server, which advertises them in
		// /api/info.
		add(Var{
			Name: "CONFIGHUB_UI_OAUTH_CLIENT_ID", Placement: InUIConfigMap, Value: kc.UIClientID,
			Doc: "The client the UI signs in as. Without it the UI can only be entered with `cub auth browser-session`.",
		})
		add(Var{
			Name: "CONFIGHUB_AUTH_ISSUER", Placement: InConfigMap, Value: kc.Issuer(),
			Doc: "Realm the UI runs OIDC against, and the issuer of the tokens the server exchanges.",
		})
		add(Var{
			Name: "CONFIGHUB_TOKEN_EXCHANGE_AUDIENCE", Placement: InConfigMap, Value: kc.Audience(),
			Doc: "Audience this instance requires in a token it will exchange; the UI client emits it.",
		})

		clientKey, generated, err := keep(KeycloakClientKeyEnv, GenerateClientKey)
		if err != nil {
			return nil, fmt.Errorf("resolving the Keycloak client key: %w", err)
		}
		// The server's whole credential. Replacing it would leave Keycloak
		// holding the public half of a key nobody signs with any more, so it is
		// preserved across re-runs like every other generated value.
		add(Var{
			Name: KeycloakClientKeyEnv, Placement: InSecret, Value: clientKey, Generated: generated,
			Doc: "Signs this server's assertions to Keycloak. Replaces both a client secret and an admin password.",
		})

		bootstrapPassword, generated, err := keep(keycloakBootstrapPasswordVar, RandomSecret)
		if err != nil {
			return nil, fmt.Errorf("resolving the Keycloak admin password: %w", err)
		}
		add(Var{
			Name: keycloakBootstrapPasswordVar, Placement: InKeycloakSecret, Value: bootstrapPassword, Generated: generated,
			Doc: "Keycloak's own console admin, set on its first start and ignored on every later one.",
		})
	}

	// --- Secret -----------------------------------------------------------

	signingKey, generated, err := keep("JWT_PRIVATE_KEY_JWK", GenerateSigningKey)
	if err != nil {
		return nil, fmt.Errorf("resolving the token signing key: %w", err)
	}
	add(Var{
		Name: "JWT_PRIVATE_KEY_JWK", Placement: InSecret, Value: signingKey, Generated: generated,
		Doc: "Signs every ConfigHub token. Unset, the server mints a new one per start and every restart logs everyone out.",
	})

	masterSecret, generated, err := keep("WORKER_MASTER_SECRET", RandomSecret)
	if err != nil {
		return nil, fmt.Errorf("resolving the worker master secret: %w", err)
	}
	add(Var{
		Name: "WORKER_MASTER_SECRET", Placement: InSecret, Value: masterSecret, Generated: generated,
		Doc: "Keys the HMAC over worker credentials. Changing it invalidates every enrolled worker.",
	})

	// DATABASE_URL is a secret whenever it embeds a password, which is whenever
	// it is real. Treating it as non-secret relies on the password not being
	// one, which stops holding the moment the database matters.
	switch opts.Database {
	case DatabaseInternal:
		password, _, err := keep(internalDBPasswordVar, RandomSecret)
		if err != nil {
			return nil, fmt.Errorf("resolving the database password: %w", err)
		}
		// Carried in the surface so the database manifest and the URL cannot
		// disagree, and marked secret so it is never rendered into a ConfigMap.
		add(Var{
			Name: internalDBPasswordVar, Placement: InSecret, Value: password,
			Doc: "Password for the bundled Postgres. Read by the database container, not by the server.",
		})
		add(Var{
			Name: "DATABASE_URL", Placement: InSecret,
			Value: fmt.Sprintf("postgresql://confighub:%s@confighub-postgres:5432/confighub?sslmode=disable", password),
			Doc:   "Connection string for the bundled Postgres.",
		})
	case DatabaseExternal:
		add(Var{
			Name: "DATABASE_URL", Placement: InSecret, Value: opts.DatabaseURL,
			Doc: "Connection string for the database this instance was pointed at.",
		})
	}

	return s, nil
}

// internalDBPasswordVar is the bundled database's password. The server does not
// read it -- Postgres does, via a secretKeyRef into the same Secret -- but it
// lives in the surface so that one generated value reaches both the database
// manifest and DATABASE_URL, and so re-runs preserve it like any other generated
// value. It must therefore be rendered into the Secret, not filtered out of it,
// or the secretKeyRef points at a key that does not exist.
const internalDBPasswordVar = "POSTGRES_PASSWORD"
