package install

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// Variables an instance still sets that the server it is moving to ignores.
//
// A generated install never has any: config.Build emits only what the server
// reads. An instance deployed from manifests someone edited, or installed by an
// older release of this plugin, can carry them for years, and nothing fails to
// say so. Upgrade names them and leaves them in place: they are in the
// operator's manifests or secret manager, which this command does not own, and
// an unread variable does no harm.

// retiredServerEnv is each variable a server once read, with the first release
// that ignores it. Only names a deployment could plausibly still set belong here.
var retiredServerEnv = []struct {
	name  string
	since [3]int
}{
	// The bridge's h2c transport.
	{"ENABLE_BRIDGE_WORKER_SERVER", [3]int{0, 2, 11}},
	{"WORKER_EXTERNAL_PORT", [3]int{0, 2, 11}},
	{"CONFIGHUB_ORG_OPERATIONS", [3]int{0, 9, 10}},
	// Workers, and the key login at /auth/worker whose audience was configurable.
	{"WORKER_MASTER_SECRET", [3]int{0, 10, 0}},
	{"CONFIGHUB_ASSERTION_AUDIENCE", [3]int{0, 10, 0}},
	{"ATTIO_API_KEY", [3]int{0, 10, 1}},
}

// envSource is where the server's container gets variables from: its own env
// list, or a ConfigMap or Secret it reads with envFrom.
type envSource struct {
	kind string // "Deployment", "ConfigMap" or "Secret"
	name string
	keys []string
}

func (s envSource) String() string { return s.kind + " " + s.name }

// unusedVar is one finding: a variable, where it is set, and what to do about it.
type unusedVar struct {
	name, source, advice string
}

// unusedEnv finds the variables in sources that a server at version target does
// not read, and the legacy name of the token signing key, which it still reads
// but which is either shadowed or due a rename.
func unusedEnv(sources []envSource, target [3]int) []unusedVar {
	setIn := map[string][]string{}
	for _, s := range sources {
		for _, k := range s.keys {
			setIn[k] = append(setIn[k], s.String())
		}
	}

	var out []unusedVar
	for _, r := range retiredServerEnv {
		if olderThan(target, r.since) {
			continue
		}
		for _, where := range setIn[r.name] {
			out = append(out, unusedVar{r.name, where,
				fmt.Sprintf("not read since v%d.%d.%d; remove it", r.since[0], r.since[1], r.since[2])})
		}
	}
	for _, where := range setIn[config.LegacySigningKeyEnv] {
		advice := "rename it to " + config.SigningKeyEnv + "; the server reads the old name only as a fallback"
		if len(setIn[config.SigningKeyEnv]) > 0 {
			advice = config.SigningKeyEnv + " is set too and wins; remove this one"
		}
		out = append(out, unusedVar{config.LegacySigningKeyEnv, where, advice})
	}
	return out
}

// unusedEnvLines formats findings for the upgrade report.
func unusedEnvLines(found []unusedVar, unreadable []string) []string {
	var lines []string
	if len(found) > 0 {
		lines = append(lines, "",
			"The instance sets variables the new version does not need. They do no harm,",
			"and nothing here changes them. Consider removing them where you set them:",
			"")
		width := 0
		for _, f := range found {
			width = max(width, len(f.name))
		}
		for _, f := range found {
			lines = append(lines, fmt.Sprintf("  %-*s  %s: %s", width, f.name, f.source, f.advice))
		}
	}
	for _, u := range unreadable {
		lines = append(lines, "", "Could not check "+u+" for unused variables.")
	}
	return lines
}

// readServerEnv reads every variable the server's container is given, by name.
// Values are never kept or printed. A ConfigMap or Secret that cannot be read
// (missing, or not readable with these credentials) is returned in unreadable
// rather than failing the upgrade, which does not depend on it.
func readServerEnv(ctx context.Context, kube kubeEnv, namespace string) (sources []envSource, unreadable []string, err error) {
	out, err := run(ctx, toolKubectl.name, kube.args("-n", namespace, "get", "deployment", serverDeploymentName, "-o", "json")...)
	if err != nil {
		return nil, nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, serverDeploymentName, err)
	}
	var d struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name string `json:"name"`
						} `json:"env"`
						EnvFrom []struct {
							ConfigMapRef *struct {
								Name string `json:"name"`
							} `json:"configMapRef"`
							SecretRef *struct {
								Name string `json:"name"`
							} `json:"secretRef"`
						} `json:"envFrom"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, serverDeploymentName, err)
	}

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name != serverDeploymentName {
			continue
		}
		direct := envSource{kind: "Deployment", name: serverDeploymentName}
		for _, e := range c.Env {
			direct.keys = append(direct.keys, e.Name)
		}
		if len(direct.keys) > 0 {
			sources = append(sources, direct)
		}
		for _, f := range c.EnvFrom {
			var s envSource
			switch {
			case f.ConfigMapRef != nil:
				s = envSource{kind: "ConfigMap", name: f.ConfigMapRef.Name}
			case f.SecretRef != nil:
				s = envSource{kind: "Secret", name: f.SecretRef.Name}
			default:
				continue
			}
			// A template that prints the keys only, so a Secret's values never
			// reach this process's output.
			keys, err := run(ctx, toolKubectl.name, kube.args("-n", namespace, "get", strings.ToLower(s.kind), s.name,
				"-o", `go-template={{range $k, $v := .data}}{{$k}}{{"\n"}}{{end}}`)...)
			if err != nil {
				unreadable = append(unreadable, s.String())
				continue
			}
			s.keys = strings.Fields(keys)
			sort.Strings(s.keys)
			sources = append(sources, s)
		}
	}
	return sources, unreadable, nil
}
