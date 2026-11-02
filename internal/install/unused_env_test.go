package install

import (
	"reflect"
	"strings"
	"testing"
)

func TestUnusedEnv(t *testing.T) {
	sources := []envSource{
		{kind: "ConfigMap", name: "confighub-env", keys: []string{"CONFIGHUB_UI_URL", "WORKER_EXTERNAL_PORT"}},
		{kind: "Secret", name: "confighub-secret-env", keys: []string{"DATABASE_URL", "WORKER_MASTER_SECRET", "JWT_PRIVATE_KEY_JWK"}},
		{kind: "Deployment", name: "confighub", keys: []string{"ATTIO_API_KEY"}},
	}

	var got []string
	for _, f := range unusedEnv(sources, [3]int{0, 10, 1}) {
		got = append(got, f.name+" @ "+f.source)
	}
	want := []string{
		"WORKER_EXTERNAL_PORT @ ConfigMap confighub-env",
		"WORKER_MASTER_SECRET @ Secret confighub-secret-env",
		"ATTIO_API_KEY @ Deployment confighub",
		"JWT_PRIVATE_KEY_JWK @ Secret confighub-secret-env",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unusedEnv =\n  %v\nwant\n  %v", got, want)
	}
}

// A variable is reported only once the target version no longer reads it.
func TestUnusedEnvFollowsTheTargetVersion(t *testing.T) {
	sources := []envSource{{kind: "Secret", name: "s", keys: []string{"ATTIO_API_KEY", "WORKER_MASTER_SECRET"}}}
	got := unusedEnv(sources, [3]int{0, 10, 0})
	if len(got) != 1 || got[0].name != "WORKER_MASTER_SECRET" {
		t.Errorf("unusedEnv at v0.10.0 = %+v, want only WORKER_MASTER_SECRET", got)
	}
}

// The legacy signing key name is still read as a fallback, so the advice depends
// on whether the current name is set alongside it.
func TestUnusedEnvLegacySigningKey(t *testing.T) {
	alone := unusedEnv([]envSource{{kind: "Secret", name: "s", keys: []string{"JWT_PRIVATE_KEY_JWK"}}}, [3]int{0, 10, 1})
	if len(alone) != 1 || !strings.HasPrefix(alone[0].advice, "rename it to CONFIGHUB_TOKEN_SIGNING_KEY_JWK") {
		t.Errorf("legacy name alone: %+v, want advice to rename", alone)
	}
	both := unusedEnv([]envSource{
		{kind: "Secret", name: "s", keys: []string{"JWT_PRIVATE_KEY_JWK", "CONFIGHUB_TOKEN_SIGNING_KEY_JWK"}},
	}, [3]int{0, 10, 1})
	if len(both) != 1 || !strings.HasSuffix(both[0].advice, "remove this one") {
		t.Errorf("both names: %+v, want advice to remove the legacy one", both)
	}
}

func TestUnusedEnvLines(t *testing.T) {
	if lines := unusedEnvLines(nil, nil); len(lines) != 0 {
		t.Errorf("nothing found printed %q", lines)
	}
	lines := unusedEnvLines([]unusedVar{{"WORKER_MASTER_SECRET", "Secret s", "not read since v0.10.0; remove it"}}, []string{"Secret other"})
	text := strings.Join(lines, "\n")
	for _, want := range []string{"WORKER_MASTER_SECRET  Secret s: not read since v0.10.0; remove it", "Could not check Secret other"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}
