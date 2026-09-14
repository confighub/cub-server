package install

// DefaultImageVersion is the server version used when the registry cannot be
// asked which is newest.
//
// Not the version an install normally gets: that is resolved from the registry
// at install time, so this plugin does not have to be re-released every time the
// server is (see imageresolve.go). This is the offline answer, and the only cost
// of being out of date here is a slightly older server for someone installing
// without a network.
//
// Overridable at build time with -ldflags "-X ...install.DefaultImageVersion=v1.2.3".
var DefaultImageVersion = "v0.4.20"

// DefaultImageRepo is public on ghcr.io, so no registry credentials are needed
// for an evaluation install. A mirror can be named with --image.
const DefaultImageRepo = "ghcr.io/confighubai/confighub"

func defaultImage() string {
	return DefaultImageRepo + ":" + DefaultImageVersion
}
