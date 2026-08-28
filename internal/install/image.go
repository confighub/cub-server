package install

// DefaultImageVersion is the ConfigHub server version installed when none is
// named.
//
// Pinned rather than "latest": an installer that resolves a moving tag gives two
// people running the same command on the same day two different instances, and
// makes "what did I install" unanswerable after the fact. Bumped deliberately,
// as part of releasing cub-server.
//
// Overridable at build time with -ldflags "-X ...install.DefaultImageVersion=v1.2.3".
var DefaultImageVersion = "v0.4.2"

// DefaultImageRepo is public on ghcr.io, so no registry credentials are needed
// for an evaluation install. A mirror can be named with --image.
const DefaultImageRepo = "ghcr.io/confighubai/confighub"

func defaultImage() string {
	return DefaultImageRepo + ":" + DefaultImageVersion
}
