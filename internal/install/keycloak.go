package install

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/confighub/cub-server/internal/config"
)

// Chapter two: adding an identity provider to an instance that is already
// running.
//
// A separate command rather than a flag on install, because it is a separate
// decision made at a separate time. You bring an instance up and look at it;
// later, you decide other people should be in it. Folding that into the first
// command would mean answering questions about identity before knowing whether
// the thing is worth keeping.
//
// It is additive, which is the property that makes it safe to run. The local
// administrator's key keeps working: the server decides on that key's presence,
// not on whether an identity provider exists, so what this adds is a second way
// in. The first then becomes the break-glass credential -- worth having,
// because an identity provider is a thing that can break.
//
// Everything it writes goes through the same generator as the first install. The
// Keycloak variables join the surface, the ConfigMap and Secret are re-rendered
// from it, and every generated value already in them is preserved. So this is a
// second pass over one configuration rather than a second configuration.

// RunKeycloak installs the bundled identity provider into an existing instance.
func RunKeycloak(ctx context.Context, u UI, o *Options) error {
	if err := o.Defaults(); err != nil {
		return err
	}
	if o.Keycloak == nil {
		return fmt.Errorf("no Keycloak configuration")
	}

	// Validated last, not first. Keycloak's addresses are derived from ports
	// this instance already publishes, so there is nothing to check until they
	// have been read off the previous render.
	if err := requireInstalled(o); err != nil {
		return err
	}
	// The instance already exists, so resolvePorts reads its ports back rather
	// than choosing any.
	if err := o.resolvePorts(); err != nil {
		return err
	}
	keycloakAddresses(o)
	if err := o.Validate(); err != nil {
		return err
	}
	if err := preflight(ctx, u, o); err != nil {
		return err
	}

	files, err := generateKeycloak(u, o)
	if err != nil {
		return err
	}

	if o.DryRun {
		u.section("Dry run: nothing was changed in the cluster.",
			"manifests   "+o.OutDir,
			"",
			"Re-run without --dry-run to apply this configuration.",
		)
		return nil
	}

	kube, err := provisionCluster(ctx, u, o)
	if err != nil {
		return err
	}

	// In two passes, and the order is the whole point. The server reads its
	// identity provider at startup and treats an unreachable one as fatal, so
	// applying its new configuration in the same breath as Keycloak restarts it
	// into a Keycloak that is still importing a realm. It recovers -- the crash
	// loop ends when Keycloak answers -- but an install that spends a minute
	// looking broken is one people stop trusting, and a rollout that waits on a
	// crash-looping pod can time out before it ever gets there.
	keycloakFiles, serverFiles := splitAtServer(files)

	if err := applyManifests(ctx, u, o, kube, keycloakFiles, "Installing Keycloak"); err != nil {
		return err
	}
	u.detail("waiting for it to import the realm, which takes a minute on a first start")
	if err := kube.waitForRollout(ctx, o.Namespace, "statefulset", config.KeycloakServiceName, rolloutTimeout); err != nil {
		return err
	}

	if err := applyManifests(ctx, u, o, kube, serverFiles, "Pointing the server at it"); err != nil {
		return err
	}
	if err := kube.waitForRollout(ctx, o.Namespace, "deployment", "confighub", rolloutTimeout); err != nil {
		return err
	}
	u.detail("api at %s...", o.APIURL())
	if err := waitForAPI(ctx, o.APIURL(), readyTimeout); err != nil {
		return err
	}

	return reportKeycloak(u, o)
}

// keycloakAddresses derives the two URLs, now that the instance's own ports are
// known.
//
// Both are overridable, because a cluster the caller brought is reached however
// that cluster is reached, and the public URL in particular has to be the
// address people really use: it is the issuer in every token.
func keycloakAddresses(o *Options) {
	if o.Keycloak.NodePort == 0 {
		o.Keycloak.NodePort = o.KeycloakNodePort
	}
	if o.Keycloak.PublicURL == "" {
		o.Keycloak.PublicURL = fmt.Sprintf("http://localhost:%d", o.Keycloak.NodePort)
	}
	if o.Keycloak.RedirectURI == "" {
		o.Keycloak.RedirectURI = o.APIURL() + "/auth/callback"
	}
	o.Keycloak.Defaults()
}

// splitAtServer divides the rendered files into what must exist before the
// server restarts and the Deployment that restarting means.
//
// Everything else -- the ConfigMap, the Secret, Keycloak itself -- can be
// applied freely: the running server does not re-read them, so it keeps serving
// on its old configuration until its Deployment is replaced.
func splitAtServer(files []config.File) (before, server []config.File) {
	for _, f := range files {
		if strings.HasSuffix(f.Path, "40-deployment.yaml") {
			server = append(server, f)
			continue
		}
		before = append(before, f)
	}
	return before, server
}

// keycloakManifest is where an instance's identity provider is recorded, and so
// how any command can tell that it has one.
func keycloakManifest(outDir string) string {
	return filepath.Join(outDir, config.ConfigDir, "25-keycloak.yaml")
}

// refuseIfKeycloakInstalled stops `cub server install` from re-rendering an
// instance that has an identity provider.
//
// Chapter one renders from a surface with no Keycloak in it, so re-running it
// here would drop the KEYCLOAK_* variables from the ConfigMap and the client key
// from the Secret. The instance would then be pointed at nothing, and worse, the
// key would be gone: Keycloak does not re-import a realm it already has, so the
// public half it is holding could never be matched again. Losing that key is
// unrecoverable in the same way losing the administrator's is.
//
// Chapter two re-renders everything chapter one does, so it is the command to
// use, not a workaround for this one.
func refuseIfKeycloakInstalled(o *Options) error {
	if _, err := os.Stat(keycloakManifest(o.OutDir)); err != nil {
		return nil // no identity provider, which is the ordinary case
	}
	return fmt.Errorf(
		"%s has an identity provider, and re-installing would discard the key it authenticates with.\n"+
			"    Keycloak does not re-import a realm it already has, so that key cannot be replaced afterwards.\n\n"+
			"    To re-render this instance, including everything `install` would have:\n"+
			"      cub server keycloak install%s",
		o.OutDir, outDirFlag(o))
}

// requireInstalled refuses to add an identity provider to nothing.
//
// The generated manifests are how this install remembers itself, and chapter two
// re-renders them. Without them there is no instance to add to, and proceeding
// would produce a second, conflicting configuration rather than an error.
func requireInstalled(o *Options) error {
	cmPath := filepath.Join(o.OutDir, config.ConfigDir, "20-configmap.yaml")
	if _, err := os.Stat(cmPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"no instance was installed from %s.\n"+
					"    `cub server keycloak install` adds an identity provider to an instance that already exists.\n\n"+
					"    To create one:\n"+
					"      cub server install%s",
				o.OutDir, outDirFlag(o))
		}
		return err
	}
	return nil
}

// generateKeycloak re-renders the instance with the identity provider added.
func generateKeycloak(u UI, o *Options) ([]config.File, error) {
	u.step("Generating configuration")

	prior, priorAdminJWK, priorImage, err := readPrior(o.OutDir)
	if err != nil {
		return nil, err
	}

	// The image this instance is on, not whatever is newest. Adding an identity
	// provider is not an upgrade, and quietly making it one would mean a command
	// about login changed the server underneath it.
	if priorImage == "" {
		return nil, fmt.Errorf("could not tell which image %s is running; re-run `cub server install` first", o.OutDir)
	}
	o.Image = priorImage

	opts := o.deploymentOptions()
	opts.AdminPublicJWK = priorAdminJWK

	surface, err := config.Build(opts, prior)
	if err != nil {
		return nil, err
	}

	files, err := config.Render(surface, opts)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		path := filepath.Join(o.OutDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		mode := os.FileMode(0o644)
		if f.Sensitive {
			mode = 0o600
		}
		if err := os.WriteFile(path, f.Content, mode); err != nil {
			return nil, err
		}
	}

	u.detail("realm %q, client %q", opts.Keycloak.Realm, opts.Keycloak.ClientID)
	u.detail("the server authenticates with a key; no client secret and no admin password exist")
	u.detail("wrote %d files to %s", len(files), o.OutDir)
	return files, nil
}

// reportKeycloak says what exists now and what to do with it.
func reportKeycloak(u UI, o *Options) error {
	k := o.Keycloak

	password, err := secretValue(o.OutDir, "KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD")
	if err != nil {
		return err
	}

	u.section("Identity provider installed.",
		"ConfigHub    "+o.APIURL(),
		"Keycloak     "+k.PublicURL,
		"realm        "+k.Realm,
		"",
		"Add people in the Keycloak console, as members of the "+k.Org+" organization:",
		"",
		"  "+k.PublicURL+"/admin",
		"  username   "+k.AdminUser,
		"  password   "+password,
		"",
		"A user who is not a member of an organization can sign in and will then be",
		"told their account is pending approval, so the membership is the part that",
		"matters.",
		"",
		"Your administrator key still works, and is now the way back in if Keycloak",
		"is ever unavailable:",
		"",
		"  cub auth login --private-key "+o.AdminKeyName+" --server "+o.APIURL(),
		"  cub auth browser-session",
	)
	return nil
}

// secretValue reads one generated value back out of the rendered Secret.
//
// Read from the file rather than kept in memory from the render, so that a
// re-run -- which preserves the value rather than generating it -- reports the
// password that is actually in effect instead of nothing.
func secretValue(outDir, key string) (string, error) {
	prior, _, _, err := readPrior(outDir)
	if err != nil {
		return "", err
	}
	value, ok := prior[key]
	if !ok {
		return "", fmt.Errorf("%s is not in the generated secret", key)
	}
	return value, nil
}
