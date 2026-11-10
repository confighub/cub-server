package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ValidateOCIURL accepts what the server accepts for OCI_EXTERNAL_URL: empty, or
// an http or https URL with a host, an optional port and nothing else. The
// server refuses to start with anything else, so it is refused here, before a
// cluster is created, rather than as a pod in CrashLoopBackOff. A path would be
// dropped by every client that builds /v2/ URLs from it.
func ValidateOCIURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("--oci-url must be an http or https URL with no path, like https://oci.example.com or https://oci.example.com:8443; got %q", raw)
	}
	return nil
}

// dnsSubdomain is what Kubernetes accepts as the name of most objects, a
// StorageClass's and a Secret's among them.
var dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// ValidateStorageClass accepts empty, which leaves the claim to the cluster's
// default StorageClass, or a name Kubernetes would accept for one. Whether the
// cluster has a class of that name is the cluster's to say.
func ValidateStorageClass(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if len(name) > 253 || !dnsSubdomain.MatchString(name) {
		return fmt.Errorf("--storage-class %q is not a StorageClass name: lower-case letters, digits, '-' and '.'", name)
	}
	return nil
}

// storageQuantity is the part of Kubernetes' quantity syntax a volume size is
// written in: a positive number with an optional binary or decimal suffix.
var storageQuantity = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|k|M|G|T|P|E)?$`)

// ValidateStorageSize accepts empty, which is DefaultStorageSize, or a size
// like 20Gi. Refused here rather than by the API server, whose message names
// the StatefulSet's field rather than the flag.
func ValidateStorageSize(size string) error {
	size = strings.TrimSpace(size)
	if size == "" {
		return nil
	}
	m := storageQuantity.FindStringSubmatch(size)
	// A zero-sized volume matches the syntax and provisions nothing.
	if m == nil || strings.Trim(strings.TrimSuffix(size, m[2]), "0.") == "" {
		return fmt.Errorf("--storage-size %q is not a volume size like 10Gi or 500Mi", size)
	}
	return nil
}

// DefaultStorageSize is the bundled database's volume when nothing says
// otherwise.
const DefaultStorageSize = "10Gi"

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

	// UIImage is the web UI's image, tag included. The UI is released with the
	// server under the same version, so the two are chosen together.
	UIImage string

	// APIURL is where a browser reaches the API. The UI is served from its own
	// origin and calls the API from the browser, so it has to be told.
	APIURL string

	// UIURL is where a browser reaches the UI. The server advertises it, so
	// clients that start from the API can send people to the UI.
	UIURL string

	// OCIURL is where clients reach the OCI registry, such as
	// https://oci.example.com, when that is not what the server can work out
	// from a request: a registry behind a load balancer that terminates TLS
	// without forwarding headers, or on a name other than oci.<API host>. The
	// server advertises it at /api/info, so it is what `cub cluster attach`
	// writes into a controller's credentials, and the token realm follows it.
	// Empty leaves the server to work the address out.
	OCIURL string

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

	// StorageClass is the StorageClass the internal database's volume claim
	// names. Empty leaves storageClassName unset, so the cluster's default
	// class provisions it -- which kind has, and many production clusters,
	// EKS Auto Mode among them, do not. There the claim stays Pending and the
	// database never starts. Never "": that asks for no dynamic provisioning
	// at all, which is a different thing.
	StorageClass string

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

	// UINodePort does the same for the web UI. Zero leaves it ClusterIP.
	UINodePort int

	// Metrics puts Prometheus's scrape annotations on the server's pod, so a
	// Prometheus that discovers pods by annotation scrapes /internal/metrics
	// on the API's port. The server serves it either way; this only says
	// where. See MetricsAnnotations.
	Metrics bool

	// ExistingSecret names a Secret the operator provides, which the server
	// and the bundled database read instead of the one this package renders:
	// one that External Secrets, or the Secrets Store CSI driver's
	// secretObjects, keeps in step with a secret store. The workloads name it,
	// and the values this package generates are rendered only for the
	// operator to load into that store, never to be applied. Empty is the
	// rendered Secret, DefaultSecretName.
	ExistingSecret string

	// Keycloak is the bundled identity provider, or nil for an instance whose
	// only identity is the bootstrap administrator's key. Nil is the first
	// install; `cub server keycloak install` is what sets it. See keycloak.go.
	Keycloak *Keycloak
}

// DefaultSecretName is the Secret the server reads its secrets from when the
// operator does not provide one. ConfigHub's installer package names it too.
const DefaultSecretName = "confighub-secret-env"

// SecretName is the Secret the workloads read: the operator's, or the one this
// package renders.
func (o *Options) SecretName() string {
	if o.ExistingSecret != "" {
		return o.ExistingSecret
	}
	return DefaultSecretName
}

// ValidateSecretName accepts empty, which is the rendered Secret, or a name
// Kubernetes would accept for one.
func ValidateSecretName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 253 || !dnsSubdomain.MatchString(name) {
		return fmt.Errorf("--existing-secret %q is not a Secret name: lower-case letters, digits, '-' and '.'", name)
	}
	return nil
}

// Defaults fills in what the caller did not specify.
//
// The defaults are chosen for the case this exists to serve: someone bringing up
// a self-hosted instance to look at it. Self-contained database, no ingress, no
// node selector -- nothing that assumes anything about the cluster beyond it
// being a cluster.
func (o *Options) Defaults() {
	if o.Namespace == "" {
		o.Namespace = "confighub"
	}
	if o.Database == "" {
		o.Database = DatabaseInternal
	}
	if o.Ingress == "" {
		o.Ingress = IngressNone
	}
	if o.StorageSize == "" {
		o.StorageSize = DefaultStorageSize
	}
	if o.DatabaseImage == "" {
		o.DatabaseImage = DefaultDatabaseImage
	}
	if o.Keycloak != nil {
		o.Keycloak.Defaults()
	}
}

// Validate rejects combinations that cannot produce a working install.
//
// Checked here rather than at apply time: a mistake caught now is a message in
// the terminal the operator is already looking at, and a mistake caught later is
// a pod in CrashLoopBackOff.
func (o *Options) Validate() error {
	// With a Secret the operator provides, DATABASE_URL can be in it rather
	// than on the command line, where it would be written to disk.
	if o.Database == DatabaseExternal && o.DatabaseURL == "" && o.ExistingSecret == "" {
		return fmt.Errorf("an external database needs its URL: pass --database-url, or put DATABASE_URL in the Secret --existing-secret names")
	}
	if err := ValidateSecretName(o.ExistingSecret); err != nil {
		return err
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
	ports := map[string]int{"--node-port": o.APINodePort, "--oci-node-port": o.OCINodePort, "--ui-node-port": o.UINodePort}
	seen := map[int]string{}
	for _, name := range []string{"--node-port", "--oci-node-port", "--ui-node-port"} {
		port := ports[name]
		if port == 0 {
			continue
		}
		if port < 30000 || port > 32767 {
			return fmt.Errorf("%s %d is outside the NodePort range 30000-32767", name, port)
		}
		if other, ok := seen[port]; ok {
			return fmt.Errorf("%s and %s cannot be the same port", other, name)
		}
		seen[port] = name
	}

	if o.Ingress == IngressTraefik && o.APINodePort != 0 {
		return fmt.Errorf("--ingress=traefik routes through the ingress controller; --node-port would be a second, unrouted way in")
	}
	if err := ValidateOCIURL(o.OCIURL); err != nil {
		return err
	}
	if o.Database == DatabaseExternal && o.StorageClass != "" {
		return fmt.Errorf("--storage-class applies to the bundled database; an external one has no volume here")
	}
	if err := ValidateStorageClass(o.StorageClass); err != nil {
		return err
	}
	if err := ValidateStorageSize(o.StorageSize); err != nil {
		return err
	}
	if o.AdminPublicJWK != "" {
		if err := ValidateAdminPublicJWK(o.AdminPublicJWK); err != nil {
			return fmt.Errorf("--admin-jwk: %w", err)
		}
	}
	if o.Keycloak != nil {
		if err := o.Keycloak.Validate(); err != nil {
			return err
		}
		if name, ok := seen[o.Keycloak.NodePort]; ok && o.Keycloak.NodePort != 0 {
			return fmt.Errorf("--keycloak-node-port %d is already %s", o.Keycloak.NodePort, name)
		}
	}
	return nil
}
