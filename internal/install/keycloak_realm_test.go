package install

import (
	"reflect"
	"testing"
)

// Keycloak adds keys of its own to what it stores, so a comparison that looked
// at whole objects would rewrite every client on every run.
func TestSubsetEqual(t *testing.T) {
	have := map[string]any{
		"clientId":   "confighub-ui",
		"attributes": map[string]any{"pkce.code.challenge.method": "S256", "added.by.keycloak": "x"},
		"webOrigins": []any{"https://ui"},
	}
	if !subsetEqual(map[string]any{
		"clientId":   "confighub-ui",
		"attributes": map[string]any{"pkce.code.challenge.method": "S256"},
		"webOrigins": []string{"https://ui"},
	}, have) {
		t.Error("a subset with equal values compared unequal")
	}
	if subsetEqual(map[string]any{"attributes": map[string]any{"pkce.code.challenge.method": "plain"}}, have) {
		t.Error("a changed nested value compared equal")
	}
	if !subsetEqual(map[string]any{"protocolMappers": []any{"ignored"}}, have, "protocolMappers") {
		t.Error("a skipped key was compared")
	}
}

// A redirect URI registered by hand survives a re-run.
func TestUnion(t *testing.T) {
	got := union([]any{"https://ui/", "https://other/"}, []any{"https://ui/", "https://new/"})
	want := []any{"https://ui/", "https://other/", "https://new/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}
