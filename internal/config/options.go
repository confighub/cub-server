package config

import (
	"fmt"
)

// DefaultDatabaseImage is the bundled database.
//
// The major version tracks what ConfigHub is run against in earnest, so that
// local development, CI, and an evaluation install all exercise the same
// database rather than three that resemble each other. A version skew here is
// the kind that surfaces as a behaviour difference nobody can reproduce.
//
// Alpine because the bundled database needs nothing that is not in it. A
// deployment that needs the image from elsewhere sets DatabaseImage; the image
// is the same, so nothing about what is being tested changes.
const DefaultDatabaseImage = "postgres:17-alpine"

// DatabaseMode says where Postgres comes from.
type DatabaseMode string

const (
	// DatabaseInternal deploys Postgres alongside the server. Self-contained,
	// which is the point for an evaluation install.
	DatabaseInternal DatabaseMode = "internal"

	// DatabaseExternal points at a database someone else runs. The caller
	// supplies the URL and nothing is deployed for it.
	DatabaseExternal DatabaseMode = "external"
)

// IngressMode says how traffic reaches the server from outside the cluster.
type IngressMode string

const (
	// IngressNone emits nothing. Reaching the server is then port-forwarding,
	// which needs no controller, no DNS, and no certificate -- the only option
	// that works on a cluster the operator just created.
	IngressNone IngressMode = "none"

	// IngressTraefik emits Traefik IngressRoutes. Requires Traefik in the
	// cluster and a hostname that resolves to it.
	IngressTraefik IngressMode = "traefik"
)

// Options describe the instance being generated for.
//
// These decide values, not just presentation, which is why they live with the
// surface rather than with the renderer: whether the database is internal
// determines whether DATABASE_URL is generated or supplied, and that is a fact
// about the configuration, not about YAML.
type Options struct {
	// Namespace everything is deployed into.
	Namespace string

	// Image is the server image, tag included.
	Image string

	// Host is the external hostname. Only meaningful with an ingress; it also
	// becomes the redirect and audience values if an IdP is ever configured.
	Host string

	Database    DatabaseMode
	DatabaseURL string // required when Database is external

	// DatabaseImage is the image for the bundled database.
	//
	// Configurable because where it is pulled from is a property of the puller,
	// not of the deployment: a CI runner shares an egress address with a great
	// many others and gets throttled by public registries, while an operator
	// pulling once does not. Same image, different registry, so pointing CI at
	// a mirror does not change what is being tested.
	DatabaseImage string

	Ingress IngressMode

	// AdminPublicJWK is the bootstrap administrator's public key. When empty the
	// generator mints a keypair and writes the private half to the CLI's key
	// store; when supplied, the operator already has one and only the public
	// half is ever seen here.
	AdminPublicJWK string

	// StorageSize for the internal database's volume.
	StorageSize string

	// APINodePort, when set, exposes the API as a NodePort on that port instead
	// of a ClusterIP.
	//
	// This is what makes a local cluster reachable without port-forwarding: a
	// kind node started with an extraPortMappings entry for the same port
	// publishes it on localhost, so the instance answers on a real URL. A
	// port-forward is a process that has to stay running and dies with the
	// terminal, which is a poor first impression of a product someone is
	// evaluating.
	//
	// Zero means ClusterIP, which is what a real deployment behind an ingress
	// wants.
	APINodePort int

	// OCINodePort does the same for the OCI registry. Zero leaves it ClusterIP
	// even when the API is a NodePort, since the registry is only needed from
	// outside the cluster for release publishing.
	OCINodePort int
}

// Defaults fills in what the caller did not specify.
//
// The defaults are chosen for the case this exists to serve: someone bringing up
// a self-hosted instance to look at it. Self-contained database, no ingress, no
// node selector -- nothing that assumes anything about the cluster beyond it
// being a cluster.
func (o *Options) Defaults(imageDefault string) {
	if o.Namespace == "" {
		o.Namespace = "confighub"
	}
	if o.Image == "" {
		o.Image = imageDefault
	}
	if o.Database == "" {
		o.Database = DatabaseInternal
	}
	if o.Ingress == "" {
		o.Ingress = IngressNone
	}
	if o.StorageSize == "" {
		o.StorageSize = "10Gi"
	}
	if o.DatabaseImage == "" {
		o.DatabaseImage = DefaultDatabaseImage
	}
}

// Validate rejects combinations that cannot produce a working install.
//
// Checked here rather than at apply time: a mistake caught now is a message in
// the terminal the operator is already looking at, and a mistake caught later is
// a pod in CrashLoopBackOff.
func (o *Options) Validate() error {
	if o.Database == DatabaseExternal && o.DatabaseURL == "" {
		return fmt.Errorf("an external database needs its URL: pass --database-url")
	}
	if o.Database == DatabaseInternal && o.DatabaseURL != "" {
		return fmt.Errorf("--database-url applies to an external database; pass --database=external to use it")
	}
	if o.Ingress == IngressTraefik && o.Host == "" {
		return fmt.Errorf("an ingress needs a hostname to route: pass --host")
	}
	switch o.Database {
	case DatabaseInternal, DatabaseExternal:
	default:
		return fmt.Errorf("unknown database mode %q: expected internal or external", o.Database)
	}
	switch o.Ingress {
	case IngressNone, IngressTraefik:
	default:
		return fmt.Errorf("unknown ingress mode %q: expected none or traefik", o.Ingress)
	}
	// Kubernetes only allocates NodePorts from a fixed range, and a value
	// outside it is rejected by the API server with a message about the range
	// rather than about the flag that produced it.
	for name, port := range map[string]int{"--node-port": o.APINodePort, "--oci-node-port": o.OCINodePort} {
		if port != 0 && (port < 30000 || port > 32767) {
			return fmt.Errorf("%s %d is outside the NodePort range 30000-32767", name, port)
		}
	}
	if o.OCINodePort != 0 && o.OCINodePort == o.APINodePort {
		return fmt.Errorf("--node-port and --oci-node-port cannot be the same port")
	}
	if o.Ingress == IngressTraefik && o.APINodePort != 0 {
		return fmt.Errorf("--ingress=traefik routes through the ingress controller; --node-port would be a second, unrouted way in")
	}
	if o.AdminPublicJWK != "" {
		if err := ValidateAdminPublicJWK(o.AdminPublicJWK); err != nil {
			return fmt.Errorf("--admin-jwk: %w", err)
		}
	}
	return nil
}
