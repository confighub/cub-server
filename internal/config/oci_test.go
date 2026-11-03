package config

import (
	"strings"
	"testing"
)

func ociTestOptions(ociURL string) Options {
	return Options{
		Namespace: "confighub", Image: "img", UIImage: "ghcr.io/confighub/ui:v0.6.5",
		APIURL: "http://localhost:32180", UIURL: "http://localhost:32183",
		APINodePort: 32180, OCINodePort: 32181, UINodePort: 32183,
		OCIURL: ociURL,
	}
}

func buildSurface(t *testing.T, opts Options, prior Preserved) *Surface {
	t.Helper()
	opts.Defaults()
	s, err := Build(opts, prior)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func inVars(vars []Var, name string) bool {
	for _, v := range vars {
		if v.Name == name {
			return true
		}
	}
	return false
}

// The URL is the server's whole answer for where the registry is. The NodePort
// beside it would override its port: the server reads OCI_EXTERNAL_PORT as an
// override of the URL's, and would advertise the load balancer's name on a port
// only the cluster's nodes answer.
func TestOCIURLReplacesTheNodePort(t *testing.T) {
	s := buildSurface(t, ociTestOptions("https://confighub-oci.example.com"), nil)
	if got := s.Get(OCIURLEnv); got != "https://confighub-oci.example.com" {
		t.Errorf("OCI_EXTERNAL_URL = %q, want the URL given", got)
	}
	if !inVars(s.ConfigMapVars(), OCIURLEnv) || inVars(s.UIConfigMapVars(), OCIURLEnv) || inVars(s.SecretVars(), OCIURLEnv) {
		t.Error("OCI_EXTERNAL_URL belongs in the server's ConfigMap and only there")
	}
	if got := s.Get("OCI_EXTERNAL_PORT"); got != "" {
		t.Errorf("OCI_EXTERNAL_PORT = %q beside OCI_EXTERNAL_URL; it would override the URL's port", got)
	}

	files := renderFiles(t, ociTestOptions("https://confighub-oci.example.com"))
	cm := files[ConfigDir+"/20-configmap.yaml"]
	if !strings.Contains(cm, "OCI_EXTERNAL_URL: https://confighub-oci.example.com") {
		t.Errorf("the rendered ConfigMap does not set OCI_EXTERNAL_URL:\n%s", cm)
	}
	if strings.Contains(cm, "OCI_EXTERNAL_PORT") {
		t.Errorf("the rendered ConfigMap sets OCI_EXTERNAL_PORT beside OCI_EXTERNAL_URL:\n%s", cm)
	}
}

// Without one, an install renders what it always has.
func TestNoOCIURLKeepsTheNodePort(t *testing.T) {
	s := buildSurface(t, ociTestOptions(""), nil)
	if got := s.Get(OCIURLEnv); got != "" {
		t.Errorf("OCI_EXTERNAL_URL = %q with none given", got)
	}
	if got := s.Get("OCI_EXTERNAL_PORT"); got != "32181" {
		t.Errorf("OCI_EXTERNAL_PORT = %q, want the NodePort", got)
	}
}

// Chosen once, at install. A re-run, or 'keycloak install', renders the
// ConfigMap again without being told, and must not drop it.
func TestOCIURLFromAPreviousRun(t *testing.T) {
	prior := Preserved{OCIURLEnv: "https://confighub-oci.example.com"}

	kept := buildSurface(t, ociTestOptions(""), prior)
	if got := kept.Get(OCIURLEnv); got != "https://confighub-oci.example.com" {
		t.Errorf("a re-run without --oci-url dropped it: OCI_EXTERNAL_URL = %q", got)
	}
	if got := kept.Get("OCI_EXTERNAL_PORT"); got != "" {
		t.Errorf("OCI_EXTERNAL_PORT = %q beside the kept OCI_EXTERNAL_URL", got)
	}

	changed := buildSurface(t, ociTestOptions("https://registry.example.com:8443"), prior)
	if got := changed.Get(OCIURLEnv); got != "https://registry.example.com:8443" {
		t.Errorf("--oci-url did not replace the previous run's: OCI_EXTERNAL_URL = %q", got)
	}
}

// The server refuses to start with anything else, so the install refuses it
// before a cluster is created.
func TestValidateOCIURL(t *testing.T) {
	for _, ok := range []string{
		"",
		"https://oci.example.com",
		"https://oci.example.com/",
		"https://oci.example.com:8443",
		"http://172.18.0.1:9092",
	} {
		opts := ociTestOptions(ok)
		opts.Defaults()
		if err := opts.Validate(); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{
		"oci.example.com",
		"ftp://oci.example.com",
		"https://",
		"https://oci.example.com/v2",
		"https://oci.example.com?x=1",
		"https://user:pass@oci.example.com",
	} {
		opts := ociTestOptions(bad)
		opts.Defaults()
		err := opts.Validate()
		if err == nil || !strings.Contains(err.Error(), "--oci-url") {
			t.Errorf("%q: want an error naming --oci-url, got %v", bad, err)
		}
	}
}
