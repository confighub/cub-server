package install

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// apiServer serves /api/info with the given registry and UI addresses, the
// way a running instance advertises them.
func apiServer(t *testing.T, tls bool, info func() apiInfo) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/info" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(info())
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

// registryServer answers /v2/ the way ConfigHub's registry does to an
// anonymous client.
func registryServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func hostPort(t *testing.T, raw string) (string, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func resultNamed(t *testing.T, results []checkResult, name string) checkResult {
	t.Helper()
	for _, r := range results {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no %s result in %+v", name, results)
	return checkResult{}
}

func TestChecksPassAgainstAHealthyInstance(t *testing.T) {
	registry := registryServer(t)
	host, port := hostPort(t, registry.URL)
	var api *httptest.Server
	api = apiServer(t, false, func() apiInfo {
		return apiInfo{Version: "v0.9.10", UIURL: api.URL + "/api/info", OCIHost: host, OCIPort: port}
	})

	results := runChecks(context.Background(), api.Client(), api.URL)
	for _, r := range results {
		if r.err != nil {
			t.Errorf("%s: %v", r.name, r.err)
		}
	}
	var out bytes.Buffer
	if err := reportChecks(UI{Out: &out}, api.URL, results); err != nil {
		t.Fatalf("reportChecks: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "v0.9.10") {
		t.Errorf("version missing from report:\n%s", out.String())
	}
}

// A hostname on a NodePort is what an install moved behind a load balancer
// keeps advertising. The hint has to name the setting that produced it.
func TestRegistryOnUnresolvableNodePortNamesTheSetting(t *testing.T) {
	api := apiServer(t, false, func() apiInfo {
		return apiInfo{Version: "v0.8.38", OCIHost: "oci.confighub-api.example.invalid", OCIPort: "32181"}
	})

	r := resultNamed(t, runChecks(context.Background(), api.Client(), api.URL), "Registry")
	if r.err == nil {
		t.Fatal("registry on an unresolvable name passed")
	}
	if r.target != "http://oci.confighub-api.example.invalid:32181" {
		t.Errorf("target = %q", r.target)
	}
	hints := strings.Join(r.hints, "\n")
	for _, want := range []string{"does not resolve", "NodePort range", "OCI_EXTERNAL_PORT", "OCI_EXTERNAL_HOST"} {
		if !strings.Contains(hints, want) {
			t.Errorf("hints lack %q:\n%s", want, hints)
		}
	}
}

// With an https API, clients reach the registry over TLS too, so a registry
// still answering plain HTTP fails for them even when it is reachable.
func TestRegistryAnsweringPlainHTTPBehindHTTPSAPI(t *testing.T) {
	registry := registryServer(t)
	host, port := hostPort(t, registry.URL)
	api := apiServer(t, true, func() apiInfo {
		return apiInfo{Version: "v0.9.10", OCIHost: host, OCIPort: port}
	})

	r := resultNamed(t, runChecks(context.Background(), api.Client(), api.URL), "Registry")
	if r.err == nil {
		t.Fatal("plain-HTTP registry behind an https API passed")
	}
	if !strings.HasPrefix(r.target, "https://") {
		t.Errorf("target = %q, want the API's scheme", r.target)
	}
	if !strings.Contains(strings.Join(r.hints, "\n"), "plain HTTP") {
		t.Errorf("hints = %q", r.hints)
	}
}

// The token endpoint behind an NLB that terminates TLS is advertised as http,
// where nothing answers: the registry passes and every pull still fails.
func TestRegistryWithUnreachableTokenRealm(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+gone.URL+`/token",service="ConfigHub OCI Registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(registry.Close)
	host, port := hostPort(t, registry.URL)
	api := apiServer(t, false, func() apiInfo { return apiInfo{OCIHost: host, OCIPort: port} })

	r := resultNamed(t, runChecks(context.Background(), &http.Client{Transport: &http.Transport{}}, api.URL), "Registry")
	if r.err == nil || !strings.Contains(r.err.Error(), "token endpoint") {
		t.Fatalf("err = %v, want the token endpoint named", r.err)
	}
	if !strings.Contains(strings.Join(r.hints, "\n"), "OCI_TOKEN_REALM") {
		t.Errorf("hints = %q", r.hints)
	}
}

func TestNoRegistryAdvertised(t *testing.T) {
	api := apiServer(t, false, func() apiInfo { return apiInfo{Version: "v0.9.10"} })
	r := resultNamed(t, runChecks(context.Background(), api.Client(), api.URL), "Registry")
	if r.err == nil {
		t.Fatal("an instance advertising no registry passed")
	}
}

// Nothing else can be checked without /api/info, so an unreachable API is the
// only result.
func TestUnreachableAPIStopsTheCheck(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	// Its own Transport: http.DefaultTransport reads the proxy environment once
	// per process, which would defeat the proxy settings of a later test.
	results := runChecks(context.Background(), &http.Client{Transport: &http.Transport{}}, srv.URL)
	if len(results) != 1 || results[0].name != "API" || results[0].err == nil {
		t.Fatalf("results = %+v", results)
	}
}

func TestDefaultPortIsLeftOut(t *testing.T) {
	api := apiServer(t, false, func() apiInfo {
		return apiInfo{OCIHost: "oci.example.invalid", OCIPort: "80"}
	})
	r := resultNamed(t, runChecks(context.Background(), api.Client(), api.URL), "Registry")
	if r.target != "http://oci.example.invalid" {
		t.Errorf("target = %q", r.target)
	}
	if strings.Contains(strings.Join(r.hints, "\n"), "NodePort") {
		t.Errorf("port 80 hinted as a NodePort: %q", r.hints)
	}
}
