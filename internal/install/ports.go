package install

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// Which host ports an instance answers on.
//
// A kind cluster publishes its ports when the node container is created and can
// never be given another, so these are decided once, before anything exists, and
// everything afterwards reads them back rather than deriving them again. Three
// sources, in this order:
//
//  1. The instance's own previous render. An instance that exists already
//     answers somewhere, and no default outranks that.
//  2. What the caller asked for. An explicit --node-port is honoured even if
//     something else holds it, so the refusal names the port rather than
//     silently choosing another.
//  3. A free port, found by asking the operating system.
//
// Rule 1 is not a nicety. Without it, re-running an install that was created on
// non-default ports re-renders it onto the defaults, moving the Services to
// ports the cluster does not publish -- an instance that is running, reachable
// at an address nothing reports, and fixed only by recreating the cluster.

// portRange is Kubernetes' NodePort range. Nothing outside it can be a
// NodePort, so nothing outside it is worth offering.
const (
	portRangeLow  = 30000
	portRangeHigh = 32767
)

// resolvePorts settles the host ports for this instance.
func (o *Options) resolvePorts() error {
	if err := o.adoptPortsFromPreviousRender(); err != nil {
		return err
	}

	// Anything still zero was neither rendered before nor asked for, so it is
	// ours to choose. taken carries the ports already spoken for in this run, so
	// two of them cannot land on the same number.
	taken := map[int]bool{}
	for _, port := range []int{o.APINodePort, o.OCINodePort, o.KeycloakNodePort, o.UINodePort} {
		if port != 0 {
			taken[port] = true
		}
	}

	for _, slot := range []struct {
		port *int
		from int
		what string
	}{
		{&o.APINodePort, DefaultAPINodePort, "the API"},
		{&o.OCINodePort, DefaultOCINodePort, "the OCI registry"},
		{&o.KeycloakNodePort, DefaultKeycloakNodePort, "an identity provider"},
		{&o.UINodePort, DefaultUINodePort, "the web UI"},
	} {
		if *slot.port != 0 {
			continue
		}
		port, err := freePortFrom(slot.from, taken)
		if err != nil {
			return fmt.Errorf("choosing a host port for %s: %w", slot.what, err)
		}
		*slot.port = port
		taken[port] = true
	}
	return nil
}

// adoptPortsFromPreviousRender fills in the ports this instance already answers
// on, leaving any the caller named explicitly alone.
//
// A missing or unreadable render is the ordinary first install, not an error.
func (o *Options) adoptPortsFromPreviousRender() error {
	content, err := os.ReadFile(filepath.Join(o.OutDir, config.ConfigDir, "50-service.yaml"))
	if err != nil {
		return nil
	}

	var ports []int
	for _, doc := range strings.Split(string(content), "\n---") {
		var svc struct {
			Spec struct {
				Ports []struct {
					NodePort int `yaml:"nodePort"`
				} `yaml:"ports"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal([]byte(doc), &svc) != nil || len(svc.Spec.Ports) == 0 {
			continue
		}
		ports = append(ports, svc.Spec.Ports[0].NodePort)
	}
	if len(ports) >= 2 {
		if o.APINodePort == 0 {
			o.APINodePort = ports[0]
		}
		if o.OCINodePort == 0 {
			o.OCINodePort = ports[1]
		}
	}
	// The UI's Service is the third. An instance rendered before the UI was its
	// own container has none, and gets a port chosen fresh -- which its cluster
	// does not publish; see requireUIPort.
	if len(ports) >= 3 && o.UINodePort == 0 {
		o.UINodePort = ports[2]
	}

	// The identity provider's port is published whether or not one is installed,
	// so it is recorded in the cluster definition rather than in a Service.
	if o.KeycloakNodePort == 0 {
		if reserved := reservedPortFromClusterDefinition(o); reserved != 0 {
			o.KeycloakNodePort = reserved
		}
	}
	return nil
}

// reservedPortFromClusterDefinition returns the published host port that is
// not the API's, the registry's or the UI's, or zero when there is no definition to
// read.
//
// Identified by elimination rather than by position, so a reordered definition
// cannot point this at the API.
func reservedPortFromClusterDefinition(o *Options) int {
	content, err := os.ReadFile(filepath.Join(o.OutDir, "kind-cluster.yaml"))
	if err != nil {
		return 0
	}
	var cluster struct {
		Nodes []struct {
			ExtraPortMappings []struct {
				HostPort int `yaml:"hostPort"`
			} `yaml:"extraPortMappings"`
		} `yaml:"nodes"`
	}
	if yaml.Unmarshal(content, &cluster) != nil || len(cluster.Nodes) == 0 {
		return 0
	}
	for _, mapping := range cluster.Nodes[0].ExtraPortMappings {
		if mapping.HostPort != o.APINodePort && mapping.HostPort != o.OCINodePort && mapping.HostPort != o.UINodePort {
			return mapping.HostPort
		}
	}
	return 0
}

// freePortFrom returns the first port at or above start that nothing is
// listening on.
//
// Free is asked of the operating system rather than assumed, because the thing
// holding a port is usually another kind cluster on this machine, and a
// collision is not reported in terms a reader can act on: kind fails with
// `docker run ... exit status 125`, which names neither the port nor the
// cluster holding it.
//
// Inherently a race -- something can take the port between this check and the
// cluster being created -- but the window is short and the alternative is a
// fixed default that is wrong whenever a second instance exists.
func freePortFrom(start int, taken map[int]bool) (int, error) {
	for port := start; port <= portRangeHigh; port++ {
		if taken[port] || !portIsFree(port) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port between %d and %d", start, portRangeHigh)
}

// portIsFree reports whether the port can be bound on IPv4.
//
// IPv4 specifically, and that is the whole point. Docker publishes a kind
// cluster's ports on 0.0.0.0, while net.Listen("tcp", ":p") binds [::]
// dual-stack -- which on macOS SUCCEEDS against a port IPv4 already holds. A
// probe written that way reports every occupied port as free and is worse than
// no probe, because it looks like it works.
func portIsFree(port int) bool {
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	listener.Close()
	return true
}
