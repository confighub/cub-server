package install

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// metadataServer is an API over TLS that advertises its token endpoint as
// tokenEndpoint says, given its own URL. Empty serves no document.
func metadataServer(t *testing.T, tokenEndpoint func(self string) string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint := tokenEndpoint(srv.URL)
		if r.URL.Path != "/.well-known/oauth-authorization-server" || endpoint == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": endpoint})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckKeyLogin(t *testing.T) {
	asHTTP := func(self string) string { return "http://" + strings.TrimPrefix(self, "https://") + "/auth/token" }
	for _, tc := range []struct {
		name          string
		tokenEndpoint func(self string) string
		fails, skips  bool
		note          string
	}{
		{name: "the address clients use", tokenEndpoint: func(self string) string { return self + "/auth/token" }},
		{name: "http behind a load balancer that terminates TLS", tokenEndpoint: asHTTP, fails: true},
		{name: "another host, as a server told its address advertises",
			tokenEndpoint: func(string) string { return "https://confighub.example.com/auth/token" },
			note:          "advertised at https://confighub.example.com/auth/token"},
		{name: "no document", tokenEndpoint: func(string) string { return "" }, skips: true,
			note: "the server does not say where its token endpoint is"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := metadataServer(t, tc.tokenEndpoint)
			r := checkKeyLogin(context.Background(), srv.Client(), srv.URL)
			if (r.err != nil) != tc.fails {
				t.Fatalf("err = %v, want a failure: %v", r.err, tc.fails)
			}
			if r.skip != tc.skips || r.note != tc.note {
				t.Errorf("skip = %v, note = %q; want %v, %q", r.skip, r.note, tc.skips, tc.note)
			}
			if !tc.fails {
				return
			}
			if !strings.Contains(r.err.Error(), "as http://") {
				t.Errorf("the failure does not say what the server sees: %v", r.err)
			}
			if hints := strings.Join(r.hints, "\n"); !strings.Contains(hints, "CONFIGHUB_EXTERNAL_URL to "+srv.URL) {
				t.Errorf("the hints do not give the setting and its value:\n%s", hints)
			}
		})
	}
}

func TestKeyLoginLines(t *testing.T) {
	asHTTP := func(self string) string { return "http://" + strings.TrimPrefix(self, "https://") + "/auth/token" }
	behindLB := metadataServer(t, asHTTP)
	direct := metadataServer(t, func(self string) string { return self + "/auth/token" })
	ctx := context.Background()

	if lines := keyLoginLines(ctx, direct.Client(), direct.URL, [3]int{0, 11, 20}); len(lines) != 0 {
		t.Errorf("a server that sees the address its clients use needs no setting:\n%s", strings.Join(lines, "\n"))
	}

	got := strings.Join(keyLoginLines(ctx, behindLB.Client(), behindLB.URL, [3]int{0, 11, 20}), "\n")
	if !strings.Contains(got, "  CONFIGHUB_EXTERNAL_URL: "+behindLB.URL) {
		t.Errorf("the plan does not give the ConfigMap entry:\n%s", got)
	}
	if strings.Contains(got, "upgrade to that release") {
		t.Errorf("v0.11.20 reads the setting, but the plan says to upgrade further:\n%s", got)
	}

	// A server that cannot be asked: the plan says when the setting is needed,
	// for an https address only.
	silent := metadataServer(t, func(string) string { return "" })
	unasked := strings.Join(keyLoginLines(ctx, silent.Client(), silent.URL, [3]int{0, 11, 20}), "\n")
	if !strings.Contains(unasked, "may need a setting") || !strings.Contains(unasked, "  CONFIGHUB_EXTERNAL_URL: "+silent.URL) {
		t.Errorf("the plan does not say when an unasked https server needs the setting:\n%s", unasked)
	}
	if lines := keyLoginLines(ctx, http.DefaultClient, "http://127.0.0.1:1", [3]int{0, 11, 20}); len(lines) != 0 {
		t.Errorf("an http address has no TLS to terminate, but the plan says:\n%s", strings.Join(lines, "\n"))
	}

	older := strings.Join(keyLoginLines(ctx, behindLB.Client(), behindLB.URL, [3]int{0, 11, 18}), "\n")
	if !strings.Contains(older, "read from v0.11.20 on") {
		t.Errorf("v0.11.18 does not read the setting, and the plan does not say so:\n%s", older)
	}
}

func TestSetsEnv(t *testing.T) {
	sources := []envSource{
		{kind: "Deployment", name: "confighub", keys: []string{"PORT"}},
		{kind: "ConfigMap", name: "confighub-env", keys: []string{"CONFIGHUB_EXTERNAL_URL", "CONFIGHUB_UI_URL"}},
	}
	if !setsEnv(sources, "CONFIGHUB_EXTERNAL_URL") || setsEnv(sources, "OCI_EXTERNAL_URL") {
		t.Error("setsEnv does not find a variable by the ConfigMap that sets it")
	}
}
