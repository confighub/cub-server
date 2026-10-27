package install

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The Keycloak release these commands are written against.
//
// Keycloak's admin API changes between minor releases in ways these commands
// depend on: 26.7 changed what organization listings return and what adding a
// member to one needs. So a Keycloak older than this is refused, and a newer
// one is used with a warning. It is the minor of DefaultKeycloakImage, which a
// test keeps in step.
const (
	keycloakMajor = 26
	keycloakMinor = 7
)

// checkKeycloakVersion refuses a Keycloak older than the release these
// commands are written against, and warns about a newer one. The version is
// read from the admin API, which the realm's own administrator can call, so
// this needs no more access than the command already has.
func checkKeycloakVersion(ctx context.Context, u UI, admin *keycloakAdmin) error {
	var info struct {
		SystemInfo struct {
			Version string `json:"version"`
		} `json:"systemInfo"`
	}
	if err := admin.doAt(ctx, "GET", "/admin/serverinfo", nil, &info); err != nil {
		u.warn("cannot read Keycloak's version, so it is not checked: %v", err)
		return nil
	}
	version := info.SystemInfo.Version
	switch keycloakVersionAge(version) {
	case keycloakOlder:
		return fmt.Errorf("Keycloak %s is older than %d.%d, the release ConfigHub is tested with; upgrade Keycloak first",
			version, keycloakMajor, keycloakMinor)
	case keycloakNewer:
		u.warn("Keycloak %s is newer than %d.%d, the release ConfigHub is tested with", version, keycloakMajor, keycloakMinor)
	case keycloakUnknown:
		u.warn("Keycloak reports version %q, which is not a release number, so it is not checked", version)
	default:
		u.detail("Keycloak %s", version)
	}
	return nil
}

type keycloakAge int

const (
	keycloakTested keycloakAge = iota
	keycloakOlder
	keycloakNewer
	keycloakUnknown
)

// keycloakVersionAge compares a version such as "26.7.3" with the tested
// major and minor. Patch releases are all the same release here.
func keycloakVersionAge(version string) keycloakAge {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return keycloakUnknown
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return keycloakUnknown
	}
	switch {
	case major < keycloakMajor || major == keycloakMajor && minor < keycloakMinor:
		return keycloakOlder
	case major > keycloakMajor || minor > keycloakMinor:
		return keycloakNewer
	default:
		return keycloakTested
	}
}
