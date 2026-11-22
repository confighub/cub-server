package install

import (
	"os"
	"testing"

	"github.com/confighub/installer/pkg/installer"
)

// Rendering with the installer re-executes the running binary for kustomize's
// transform step, which under test is this test binary.
func TestMain(m *testing.M) {
	installer.RunTransformerIfRequested()
	os.Exit(m.Run())
}
