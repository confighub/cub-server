package install

import (
	"fmt"
	"strings"
	"testing"

	"github.com/confighub/cub-server/internal/config"
)

func TestKeycloakVersionAge(t *testing.T) {
	for version, want := range map[string]keycloakAge{
		"26.7.3":           keycloakTested,
		"26.7.0":           keycloakTested,
		"26.6.1":           keycloakOlder,
		"25.0.6":           keycloakOlder,
		"26.8.0":           keycloakNewer,
		"27.0.0":           keycloakNewer,
		"999.0.0-SNAPSHOT": keycloakNewer,
		"":                 keycloakUnknown,
		"nightly":          keycloakUnknown,
	} {
		if got := keycloakVersionAge(version); got != want {
			t.Errorf("keycloakVersionAge(%q) = %d, want %d", version, got, want)
		}
	}
}

// The version these commands check for is the one an install deploys.
func TestKeycloakVersionMatchesDefaultImage(t *testing.T) {
	tag := config.DefaultKeycloakImage[strings.LastIndex(config.DefaultKeycloakImage, ":")+1:]
	if want := fmt.Sprintf("%d.%d.", keycloakMajor, keycloakMinor); !strings.HasPrefix(tag, want) {
		t.Errorf("DefaultKeycloakImage is %s; keycloakMajor and keycloakMinor say %s", tag, want)
	}
}
