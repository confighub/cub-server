package install

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// writeRender lays down the two files resolvePorts reads an existing instance's
// ports out of.
func writeRender(t *testing.T, dir string, api, oci, keycloak int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, config.ConfigDir), 0o755); err != nil {
		t.Fatal(err)
	}
	services := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: confighub-api
spec:
  type: NodePort
  ports:
  - port: 9090
    nodePort: %d
---
apiVersion: v1
kind: Service
metadata:
  name: confighub-oci-server
spec:
  type: NodePort
  ports:
  - port: 9092
    nodePort: %d
`, api, oci)
	if err := os.WriteFile(filepath.Join(dir, config.ConfigDir, "50-service.yaml"), []byte(services), 0o644); err != nil {
		t.Fatal(err)
	}

	cluster := fmt.Sprintf(`kind: Cluster
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: %d
    hostPort: %d
  - containerPort: %d
    hostPort: %d
  - containerPort: %d
    hostPort: %d
`, api, api, oci, oci, keycloak, keycloak)
	if err := os.WriteFile(filepath.Join(dir, "kind-cluster.yaml"), []byte(cluster), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An instance that already exists answers on ports of its own, and no default
// outranks that. Getting this wrong re-renders the Services onto ports the
// cluster does not publish: the instance keeps running, and nothing can reach it
// at the address the install reports.
func TestPortsComeFromThePreviousRender(t *testing.T) {
	dir := t.TempDir()
	writeRender(t, dir, 32280, 32281, 32282)

	o := &Options{OutDir: dir}
	if err := o.resolvePorts(); err != nil {
		t.Fatal(err)
	}
	if o.APINodePort != 32280 || o.OCINodePort != 32281 {
		t.Errorf("API/OCI resolved to %d/%d, want the rendered 32280/32281", o.APINodePort, o.OCINodePort)
	}
	// Published for an identity provider that may not be installed, so it is
	// recorded in the cluster definition rather than in a Service.
	if o.KeycloakNodePort != 32282 {
		t.Errorf("Keycloak port is %d, want the reserved 32282", o.KeycloakNodePort)
	}
}

// An explicit flag is honoured even where a render exists, so someone moving an
// instance on purpose can.
func TestExplicitPortsWin(t *testing.T) {
	dir := t.TempDir()
	writeRender(t, dir, 32280, 32281, 32282)

	o := &Options{OutDir: dir, APINodePort: 31000}
	if err := o.resolvePorts(); err != nil {
		t.Fatal(err)
	}
	if o.APINodePort != 31000 {
		t.Errorf("API port is %d, want the requested 31000", o.APINodePort)
	}
	if o.OCINodePort != 32281 {
		t.Errorf("OCI port is %d; naming one port should not move the others", o.OCINodePort)
	}
}

// With no instance yet, the defaults are a starting point rather than a
// requirement: a port something else holds is skipped instead of failing with
// docker's exit status 125, which names neither the port nor what holds it.
func TestFreePortsAreChosenWhenDefaultsAreTaken(t *testing.T) {
	// Staged on IPv4, the way Docker publishes. Binding the dual-stack ":p" here
	// instead would make the test agree with a probe that also binds dual-stack,
	// and the pair would pass while missing every real collision.
	blocker, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", DefaultAPINodePort))
	if err != nil {
		t.Skipf("cannot bind %d to stage the test: %v", DefaultAPINodePort, err)
	}
	defer blocker.Close()

	o := &Options{OutDir: t.TempDir()}
	if err := o.resolvePorts(); err != nil {
		t.Fatal(err)
	}
	if o.APINodePort == DefaultAPINodePort {
		t.Fatalf("chose %d, which is held by this test", o.APINodePort)
	}
	if !portIsFree(o.APINodePort) {
		// Racy in principle; it would have to be taken in the last microsecond.
		t.Errorf("chose %d, which is not free", o.APINodePort)
	}
}

// Three ports for three services, and a collision would be found only when
// Kubernetes rejected the second Service.
func TestChosenPortsAreDistinct(t *testing.T) {
	o := &Options{OutDir: t.TempDir()}
	if err := o.resolvePorts(); err != nil {
		t.Fatal(err)
	}
	ports := map[int]string{}
	for name, port := range map[string]int{
		"API": o.APINodePort, "OCI": o.OCINodePort, "Keycloak": o.KeycloakNodePort,
	} {
		if other, clash := ports[port]; clash {
			t.Errorf("%s and %s both got port %d", name, other, port)
		}
		ports[port] = name
		if port < portRangeLow || port > portRangeHigh {
			t.Errorf("%s got %d, outside the NodePort range", name, port)
		}
	}
}

// A first install has nothing to read, which is the ordinary case and not a
// failure.
func TestNoPreviousRenderIsNotAnError(t *testing.T) {
	o := &Options{OutDir: filepath.Join(t.TempDir(), "does-not-exist")}
	if err := o.resolvePorts(); err != nil {
		t.Fatalf("a first install should not error: %v", err)
	}
	if o.APINodePort == 0 || o.OCINodePort == 0 || o.KeycloakNodePort == 0 {
		t.Error("ports left unset")
	}
}
