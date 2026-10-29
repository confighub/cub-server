package install

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CheckOptions are the inputs to RunCheck.
type CheckOptions struct {
	// URL is where the API answers. Empty means the server of cub's current
	// context, which cub passes to a plugin as CUB_SERVER.
	URL string
}

// apiInfo is the part of GET /api/info a check reads: what the server says
// about where it, its UI, its registry and its identity provider are.
type apiInfo struct {
	Version    string
	UIURL      string
	OCIHost    string
	OCIPort    string
	AuthIssuer string
}

// checkResult is one address probed. A result with a nil err passed.
type checkResult struct {
	name   string
	target string
	note   string // shown after a pass, e.g. the version
	skip   bool   // nothing to check; note says why
	err    error
	hints  []string
}

// RunCheck asks a running instance where its API, UI, registry and identity
// provider are, and tries to reach each one from this machine. It changes
// nothing, reads nothing from the cluster, and works however the instance was
// deployed.
//
// What the server advertises is what every client is told: cub reads the
// registry address from /api/info and writes it into the Argo CD and Flux
// credentials of an attached cluster. An address that only worked for the
// install's defaults, such as a NodePort, keeps being advertised after the
// instance moves behind a load balancer, and nothing fails until a cluster
// tries to pull.
func RunCheck(ctx context.Context, u UI, o *CheckOptions) error {
	api := o.URL
	if api == "" {
		api = os.Getenv("CUB_SERVER")
	}
	api = strings.TrimSuffix(strings.TrimSpace(api), "/")
	if api == "" {
		return errors.New("no server to check: run it as 'cub server check' with a cub context, or pass --url")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	results := runChecks(ctx, client, api)
	return reportChecks(u, api, results)
}

// runChecks probes the API first, since everything else is read from it.
func runChecks(ctx context.Context, client *http.Client, api string) []checkResult {
	info, err := fetchAPIInfo(ctx, client, api)
	if err != nil {
		return []checkResult{{name: "API", target: api, err: err, hints: reachHints(err)}}
	}
	results := []checkResult{{name: "API", target: api, note: info.Version}}
	results = append(results, checkUI(ctx, client, info))
	results = append(results, checkRegistry(ctx, client, api, info))
	if info.AuthIssuer != "" {
		results = append(results, checkIssuer(ctx, client, info.AuthIssuer))
	}
	return results
}

func fetchAPIInfo(ctx context.Context, client *http.Client, api string) (*apiInfo, error) {
	resp, err := get(ctx, client, api+"/api/info")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/info answered HTTP %d", resp.StatusCode)
	}
	var info apiInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("GET /api/info: %w", err)
	}
	return &info, nil
}

// checkUI tries the UI address the server advertises, which is where cub sends
// a browser. An instance that advertises none still works; cub then has
// nowhere to send people.
func checkUI(ctx context.Context, client *http.Client, info *apiInfo) checkResult {
	r := checkResult{name: "UI", target: info.UIURL}
	if info.UIURL == "" {
		r.target, r.skip = "(not advertised)", true
		r.note = "set CONFIGHUB_UI_URL in the confighub-env ConfigMap so cub can open the UI"
		return r
	}
	resp, err := get(ctx, client, info.UIURL)
	if err != nil {
		r.err, r.hints = err, reachHints(err)
		return r
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		r.err = fmt.Errorf("answered HTTP %d", resp.StatusCode)
	}
	return r
}

// checkRegistry tries the OCI registry where the server says it is, the way a
// client builds the address: the API's scheme, the advertised host, and the
// advertised port unless the scheme implies it. A registry answers /v2/ with
// 200, or 401 when it wants a token first.
func checkRegistry(ctx context.Context, client *http.Client, api string, info *apiInfo) checkResult {
	r := checkResult{name: "Registry"}
	if info.OCIHost == "" {
		r.target = "(not advertised)"
		r.err = errors.New("the server advertises no OCI registry, so attached clusters have nowhere to pull from")
		return r
	}
	scheme := "https"
	if u, err := url.Parse(api); err == nil && u.Scheme == "http" {
		scheme = "http"
	}
	host := info.OCIHost
	if p := info.OCIPort; p != "" && !(scheme == "https" && p == "443") && !(scheme == "http" && p == "80") {
		host = net.JoinHostPort(host, p)
	}
	r.target = scheme + "://" + host
	resp, err := get(ctx, client, r.target+"/v2/")
	if err != nil {
		r.err = err
		r.hints = append(reachHints(err), registryHints(info)...)
		return r
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized &&
		resp.Header.Get("Docker-Distribution-Api-Version") == "" {
		r.err = fmt.Errorf("GET /v2/ answered HTTP %d, which is not a registry", resp.StatusCode)
		r.hints = registryHints(info)
	}
	return r
}

// registryHints says where a registry address comes from, so a wrong one can
// be traced to the setting that made it.
func registryHints(info *apiInfo) []string {
	hints := []string{
		"The server advertises oci.<the host the API was reached on>, or OCI_EXTERNAL_HOST, on OCI_EXTERNAL_PORT, all set in the confighub-env ConfigMap.",
	}
	if port, err := strconv.Atoi(info.OCIPort); err == nil && port >= 30000 && port <= 32767 && !isAddressHost(info.OCIHost) {
		hints = append(hints, fmt.Sprintf(
			"Port %d is in the NodePort range: probably the OCI_EXTERNAL_PORT 'cub server install' set for a local install. "+
				"Expose the registry (Service confighub-oci-server, port 9092) the way the API is exposed, then set OCI_EXTERNAL_PORT "+
				"to the port it answers on (443 behind TLS) and OCI_EXTERNAL_HOST to its name if that is not %s.", port, info.OCIHost))
	}
	return hints
}

// checkIssuer tries the identity provider's discovery document, which the
// server and every browser sign-in depend on.
func checkIssuer(ctx context.Context, client *http.Client, issuer string) checkResult {
	r := checkResult{name: "Identity", target: issuer}
	resp, err := get(ctx, client, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		r.err, r.hints = err, reachHints(err)
		return r
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.err = fmt.Errorf("discovery document answered HTTP %d", resp.StatusCode)
	}
	return r
}

func get(ctx context.Context, client *http.Client, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// reachHints turns a failed request into what to look at. The raw error is
// printed as well; these name the likely cause in deployment terms.
func reachHints(err error) []string {
	var dnsErr *net.DNSError
	var recordErr tls.RecordHeaderError
	var unknownCA x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return []string{fmt.Sprintf("%s does not resolve from here: register it in DNS.", dnsErr.Name)}
	// net/http turns the TLS record error into a message of its own when the
	// bytes look like an HTTP response, so both spellings are matched.
	case errors.As(err, &recordErr) || strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		return []string{"It answers plain HTTP where TLS was expected: terminate TLS in front of it, as for the API."}
	case errors.As(err, &unknownCA):
		return []string{"Its certificate is signed by an authority this machine does not trust. Clusters that pull from it need to trust it too."}
	case errors.As(err, &hostnameErr):
		return []string{"Its certificate does not cover this name."}
	case errors.Is(err, syscall.ECONNREFUSED):
		return []string{"Nothing listens there: check the Service and load balancer in front of it."}
	case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
		return []string{"No answer before the timeout: a firewall or security group may be dropping it, or it is reachable only from inside the network."}
	}
	return nil
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isAddressHost reports whether a host is an IP address or localhost, which
// the server advertises as it was reached rather than prefixing "oci.".
func isAddressHost(host string) bool {
	return net.ParseIP(host) != nil || strings.EqualFold(host, "localhost")
}

func reportChecks(u UI, api string, results []checkResult) error {
	u.step("Checking the instance at %s", api)
	failed := 0
	for _, r := range results {
		if r.err == nil {
			status := "ok"
			if r.skip {
				status = "--"
			}
			line := fmt.Sprintf("%-5s %-9s %s", status, r.name, r.target)
			if r.note != "" {
				line += "  (" + r.note + ")"
			}
			u.detail("%s", line)
			continue
		}
		failed++
		u.detail("FAIL  %-9s %s", r.name, r.target)
		u.detail("      %v", r.err)
		for _, h := range r.hints {
			u.detail("      %s", h)
		}
	}
	u.detail("")
	u.detail("Attached clusters pull from the registry. This checked it from this machine; a cluster in another network may not reach it.")
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(results))
	}
	return nil
}
