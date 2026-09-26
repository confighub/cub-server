package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/confighub/cub-server/internal/config"
)

// The web UI runs as its own container.
//
// It is released with the server under the same version, from its own public
// repository. The server's tags carry a "v" and the UI's do not: server v0.6.5
// goes with ui 0.6.5. So the UI image is derived from the server image rather
// than resolved separately, which keeps the two from drifting apart and needs no
// second registry lookup.

// DefaultUIImageRepo is public on ghcr.io, like the server's.
const DefaultUIImageRepo = "ghcr.io/confighub/ui"

// uiDeploymentName is the UI's Deployment, as the manifest names it.
const uiDeploymentName = "confighub-ui"

// minimumServerVersion is the oldest server this plugin installs.
//
// v0.6.5 is the first release whose server reads the auth variables under the
// names rendered here (CONFIGHUB_AUTH_ISSUER, CONFIGHUB_TOKEN_EXCHANGE_AUDIENCE)
// and no longer needs a cookie-login redirect, and whose UI is published as its
// own image. An older server with this configuration would refuse to start or
// have no UI to go with it.
var minimumServerVersion = [3]int{0, 6, 5}

// serverVersion reads vMAJOR.MINOR.PATCH from an image reference, or reports
// false for any other tag -- a local build, a digest, a branch image.
func serverVersion(image string) ([3]int, string, bool) {
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i:], "/") {
		return [3]int{}, "", false
	}
	tag := image[i+1:]
	m := semverTag.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, "", false
	}
	var v [3]int
	for k := 0; k < 3; k++ {
		n, err := strconv.Atoi(m[k+1])
		if err != nil {
			return [3]int{}, "", false
		}
		v[k] = n
	}
	return v, tag, true
}

func olderThan(a, b [3]int) bool {
	for k := 0; k < 3; k++ {
		if a[k] != b[k] {
			return a[k] < b[k]
		}
	}
	return false
}

// requireSupportedServer refuses a released server older than this plugin can
// configure. Images that are not a release are let through: a local build or a
// mirror under another tag is the caller's to vouch for.
func requireSupportedServer(image string) error {
	v, tag, ok := serverVersion(image)
	if !ok || !olderThan(v, minimumServerVersion) {
		return nil
	}
	min := fmt.Sprintf("v%d.%d.%d", minimumServerVersion[0], minimumServerVersion[1], minimumServerVersion[2])
	return fmt.Errorf(
		"%s is older than %s, the first server this plugin can install: from %s the web UI is its own container, and the server reads its sign-in settings under new names.\n"+
			"    Install a newer one with: cub server install --image %s:<version>",
		tag, min, min, DefaultImageRepo)
}

// resolveUIImage decides the UI image, and says so.
//
//  1. --ui-image wins.
//  2. Otherwise the UI release with the server's version.
//  3. Otherwise, for a server image that is not a release, the UI this instance
//     already runs.
//
// There is nothing to fall back to beyond that: a server built locally has no
// UI release to match, so the caller names one.
func resolveUIImage(u UI, o *Options) error {
	if o.UIImage != "" {
		return nil
	}
	if _, tag, ok := serverVersion(o.Image); ok {
		o.UIImage = DefaultUIImageRepo + ":" + strings.TrimPrefix(tag, "v")
		return nil
	}
	if prior := priorUIImage(o.OutDir); prior != "" {
		o.UIImage = prior
		u.detail("keeping the UI image this instance is on (%s)", prior)
		return nil
	}
	return fmt.Errorf(
		"%s is not a released version, so there is no UI release to match it.\n"+
			"    Name one: --ui-image %s:<version>",
		o.Image, DefaultUIImageRepo)
}

// priorUIImage reads the UI image from a previous render, or "" when there is
// none.
func priorUIImage(outDir string) string {
	content, err := os.ReadFile(filepath.Join(outDir, config.ConfigDir, "45-ui.yaml"))
	if err != nil {
		return ""
	}
	for _, doc := range strings.Split(string(content), "\n---") {
		var dep struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Image string `yaml:"image"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal([]byte(doc), &dep) == nil && dep.Kind == "Deployment" && len(dep.Spec.Template.Spec.Containers) > 0 {
			return dep.Spec.Template.Spec.Containers[0].Image
		}
	}
	return ""
}
