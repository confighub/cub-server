package install

import (
	"slices"
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
	writeStorageRender(t, dir, config.Options{Namespace: "confighub", APINodePort: 32180, Metrics: true})

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
	writeStorageRender(t, plain, config.Options{Namespace: "confighub", APINodePort: 32180})
	o = Options{KubeContext: "prod", OutDir: plain}
	if err := o.Defaults(); err != nil {
		t.Fatal(err)
	}
	if o.Metrics {
		t.Error("a re-run of an install without metrics turned them on")
	}
}
