package install

import (
	"slices"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

// The installer selects the metrics component only when asked, so an install
// without it renders with a package from before the component existed.
func TestInstallerArgsWithMetrics(t *testing.T) {
	opts, s := installerOptions(t, false)
	args, err := installerArgs(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(flagValues(args, "--select"), "metrics") {
		t.Errorf("selected metrics without being asked: %v", args)
	}

	opts.Metrics = true
	args, err = installerArgs(opts, s, "/pkg", "/work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(flagValues(args, "--select"), "metrics") {
		t.Errorf("--enable-metrics did not select metrics: %v", args)
	}
}

// Metrics are read back from the render, so a re-run, or 'keycloak install',
// without the flag keeps the annotations; --enable-metrics=false drops them.
func TestRerunKeepsMetrics(t *testing.T) {
	dir := t.TempDir()
	writeOptionsRender(t, dir, config.Options{Namespace: "confighub", APINodePort: 32180, Metrics: true})

	o := Options{KubeContext: "prod", OutDir: dir}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if !o.Metrics || !o.deploymentOptions().Metrics {
		t.Error("a re-run without --enable-metrics dropped the metrics annotations")
	}

	o = Options{KubeContext: "prod", OutDir: dir, MetricsSet: true}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.Metrics {
		t.Error("--enable-metrics=false kept the metrics annotations")
	}

	plain := t.TempDir()
	writeOptionsRender(t, plain, config.Options{Namespace: "confighub", APINodePort: 32180})
	o = Options{KubeContext: "prod", OutDir: plain}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.Metrics {
		t.Error("a re-run of an install without metrics turned them on")
	}
}

// A render asked for metrics that does not carry them, as from a package
// published before the metrics component, is refused, naming the way out;
// one that carries them, or was not asked for them, passes.
func TestCheckMetricsRendered(t *testing.T) {
	deployment := func(annotations string) []config.File {
		return []config.File{{Path: config.ConfigDir + "/deployment-confighub-confighub.yaml", Content: []byte(
			"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: confighub\nspec:\n  template:\n    metadata:\n" + annotations,
		)}}
	}
	annotated := deployment("      annotations:\n        prometheus.io/scrape: \"true\"\n")
	plain := deployment("      labels:\n        app: confighub\n")

	if err := checkMetricsRendered(config.Options{Metrics: true}, annotated, "/pkg"); err != nil {
		t.Errorf("refused an annotated render: %v", err)
	}
	if err := checkMetricsRendered(config.Options{}, plain, "/pkg"); err != nil {
		t.Errorf("refused a render not asked for metrics: %v", err)
	}
	err := checkMetricsRendered(config.Options{Metrics: true}, plain, "oci://ghcr.io/confighub/packages/confighub:0.10.4")
	if err == nil {
		t.Fatal("accepted a render asked for metrics that has none")
	}
	for _, want := range []string{"confighub:0.10.4", "Drop --installer", "--installer-package"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}
