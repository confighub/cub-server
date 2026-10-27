# ConfigHub self-hosted server installation plugin

> [!IMPORTANT] 
> **You probably don't need this.** To try ConfigHub, [sign
> up](https://auth.confighub.com/sign-up) for hub.confighub.com and follow the
> [tutorial](https://docs.confighub.com/get-started/tutorial/setup/). Nothing in this repository
> is required.
>
> This plugin installs a **self-hosted** ConfigHub server, for anyone evaluating
> self-hosting. Use is conditioned on the [evaluation license](EVALUATION-LICENSE.txt).

## License requirements

This plugin lets you install a self-hosted instance of ConfigHub. You must agree to and meet the requirements of the [EVALUATION LICENSE](EVALUATION-LICENSE.txt), before moving forward.

**NOTE:** The code in this repo is covered by the MIT [LICENSE](LICENSE). The evaluation license is only for the Confighub Server.

## Prerequisites

- **Docker**, running.
- **kubectl** on your PATH.

## Install

The simplest usage:

```sh
cub plugin install confighub/cub-server
cub server install -i
```

This creates a local Kubernetes cluster (using the kind Go library), deploys ConfigHub and
a database into it, waits for the instance to answer, and signs you in. Once complete, you can immediately start using ConfigHub CLI, e.g:

```
cub space list
```

You can also log into the Web UI by using the authenticated CLI to start a browser session:

```
cub auth browser-session
```

The server advertises where its UI is (`UIURL` in `/api/info`, from `CONFIGHUB_UI_URL`), and
`cub auth login` records it on the context, so this opens the UI container. That needs cub
v0.8.2 or newer.

## What it installs

Into a namespace (`confighub` by default):

| | |
|---|---|
| ConfigHub server | API on a NodePort, OCI registry on another |
| ConfigHub web UI | its own container (`ghcr.io/confighub/ui`), on a NodePort of its own |
| PostgreSQL | bundled by default; `--database=external` points at your own |
| Keycloak | not installed by default — see [Adding people](#adding-people) |

The API and the UI are published on real host ports rather than through `kubectl port-forward`,
so the URLs keep working after you close the terminal. The UI is a static app that calls the API
from the browser, so the two have different origins: by default the API is on `localhost:32180`
and the UI on `localhost:32183`.

Ports are chosen for you. kind has to publish them when the node container is created, before
there is a cluster to ask, so an install picks free ones from the operating system rather than
insisting on a default — a second instance, or anything else already holding the port, moves it
along instead of failing. Re-running an install keeps the ports the instance already answers on;
`--node-port`, `--ui-node-port`, `--oci-node-port` and `--keycloak-node-port` override both.

## Adding people

`cub server install` leaves you with an instance and one administrator, who signs in with a
key (located in ~/.confighub/keys). That is enough to evaluate ConfigHub and enough to run it alone. To support multiple users, ConfigHub relies on keycloak as an IdP and SSO broker. Install it with:

```sh
cub server keycloak install
```

Keycloak is deployed alongside the instance, a realm is imported with the clients ConfigHub
needs (the server, the CLI, and the web UI), and the server and UI are reconfigured and restarted
to use it. People then sign in on the UI through Keycloak. After installation you can log into keycloak's admin interface with:

```sh
cub server keycloak open
```

This will copy the kcadmin password to the clipboard and open your browser on the web interface. Login with user `kcadmin` and the password in the clipboard.

**This is additive.** Your administrator user and key keep working afterwards. It can be used for break-glass operations when there are issues with the keycloak setup that prevents other users from logging in:

```sh
cub auth login --private-key confighub-admin --server http://localhost:32180
cub auth browser-session
```

### Connecting your identity provider

While you can use keycloak to manage users directly, in most cases you will want to connect a ConfigHub organization to your company's identity provider using OIDC or SAML:

```sh
cub server keycloak idp -i
```

This asks for your organization's name and email domain, then for your provider's OpenID
discovery URL and the client id and secret of an application registered for ConfigHub at their
end. Okta, Entra ID, Google Workspace and Auth0 all publish a discovery document, so one URL
replaces naming each endpoint. It finishes by printing a redirect URI to register with your
provider — sign-in fails on the way back without it.

For a SAML provider, pass its metadata and the attribute carrying each person's email, then
register Keycloak at the provider from the service-provider metadata URL the command prints:

```sh
cub server keycloak idp --saml-metadata-file pingfederate.xml --alias pingfederate \
  --org "Acme" --org-domain acme.com \
  --email-attribute mail --first-name-attribute givenName --last-name-attribute sn
```

The email domain is not cosmetic. ConfigHub resolves a user's organization from their token and
shows anyone who belongs to none a pending-approval page, and the domain is what makes someone
arriving through your provider a member. That is why the organization and the provider are set
up by one command rather than two: they only produce a working login together.

### Org roles

The server reads a user's org role from their Keycloak realm roles: `admin`, `manager`,
`editor`, `user`, `viewer`, `creator` or `none`. Give each person one: with several, the server
takes the first it recognizes, in no order you control. Someone holding none gets `creator`.
Grant one to a person in the Keycloak console
(Users → Role mapping), or to a Keycloak group, or derive them from your provider:

```sh
cub server keycloak idp --alias pingfederate \
  --role-attribute memberOf \
  --role-map "CN=confighub-admins,OU=Groups,DC=acme,DC=com=admin" \
  --role-map "CN=confighub-viewers,OU=Groups,DC=acme,DC=com=viewer"
```

Mapped roles follow the provider at every sign-in, so removing someone from the group removes
the role. Re-running with a different set of `--role-map` replaces the earlier set.

### The Keycloak console

```sh
cub server keycloak open              # opens it, admin password on your clipboard
cub server keycloak open --print-url  # just the URL
```

For anything the commands above do not cover — adding a user by hand, changing realm settings.
The password is Keycloak's own administrator, set on its first start. It is not a ConfigHub
account: it opens that console and nothing else.

### What the server holds

No password and no shared secret. The server authenticates to Keycloak by signing an assertion
with an Ed25519 key generated during the install; Keycloak holds only the public half, and the
client's service account is what reaches the admin API. Keycloak's own admin password is in a
separate Secret that only Keycloak reads, so it is not in the server's environment.

The bundled Keycloak runs in development mode with an embedded database, which is what lets it
come up on a laptop with no certificate and no DNS. An instance serving real users wants a
Keycloak of its own.

### A Keycloak you run, for an instance you deployed

`keycloak install` works on an instance this plugin installed and applies everything itself. If
you deployed ConfigHub from manifests you manage (say, the output of `install --dry-run`, edited
for your cluster), and run Keycloak yourself, it takes two commands.

They need Keycloak 26.7, the release ConfigHub runs and the one `keycloak install` deploys. Each
command reads Keycloak's version and refuses an older one, since the admin API they use changed in
26.7; a newer one gets a warning.

The first is the only one that needs an administrator of the whole Keycloak:

```sh
export KEYCLOAK_ADMIN_PASSWORD=...   # a master-realm administrator
cub server keycloak bootstrap --keycloak-url https://keycloak.acme.com
```

It creates the realm with one client, the server's, which signs in with a key generated here
(Keycloak holds only the public half). That client's service account administers this realm and
nothing else on the Keycloak. The key is written to `keycloak-client-key.jwk`; whoever runs
bootstrap can hand it to whoever runs ConfigHub. A master-realm client can stand in for the admin
user (`--admin-client-id`, secret in `$KEYCLOAK_ADMIN_CLIENT_SECRET`).

Everything else signs in as the server's client, with that key:

```sh
cub server keycloak configure \
  --keycloak-url https://keycloak.acme.com \
  --ui-url https://confighub.acme.com \
  --org acme --org-domain acme.com
```

It sets up the rest of the realm: organizations on, the CLI's and the UI's clients, the
organization, and the org roles. Then it writes merge patches for the server's ConfigMap
(`confighub-env`) and Secret (`confighub-secret-env`) and the UI's ConfigMap (`confighub-ui-env`).
Merge them into your manifests, or apply them with the `kubectl patch` commands it prints. It
never touches the cluster.

If External Secrets produces the server's Secret, a patch on that Secret lasts only until the
next sync. Pass `--secrets external-secrets --remote-key <path> [--remote-property <name>]`
instead: the key is written to `keycloak-client-key.jwk` for you to put in your secret store,
and an entry for the ExternalSecret (default `confighub-secret-env`) replaces the Secret patch.
With the Secrets Store CSI driver and AWS Secrets Manager, pass `--secrets secrets-store-csi
--secret-provider-class <name> --remote-key <secret> [--remote-property <name>]`: you get the key
file, the entries to add to that SecretProviderClass, and the volume the `confighub` Deployment
needs, since the driver writes a Secret only while a pod mounts the class.

Both read the key from `~/.confighub/keycloak/<keycloak host>/<realm>` (or `--out-dir`), or from
`--client-key-file` if you keep it elsewhere. `keycloak idp` works against this Keycloak with
`--keycloak-url`, signing in the same way.

## Which server version

`cub server install` asks the registry for the newest released ConfigHub version and installs
that, so this plugin does not have to be re-released every time the server is.

The version it resolved is written into the generated manifests, not a floating tag — so what is
running is answerable, and two people installing at the same moment get the same thing.

```sh
cub server install --image ghcr.io/confighubai/confighub:v0.9.5   # pick one
```

The UI is released with the server under the same version and follows it: server `v0.9.5` gets
`ghcr.io/confighub/ui:v0.9.5`. `--ui-image` names a different one, and is required when `--image`
is not a released version. The oldest server this plugin installs is v0.9.5, the first to read the token signing key as `CONFIGHUB_TOKEN_SIGNING_KEY_JWK`.

Re-running an install keeps the version the instance is already on; it resumes rather than
upgrading. Pass `--image` to move it, which is also how you install without reaching the
registry at all.

### Pulling from gcr.io

Every release is published to ghcr.io and copied to gcr.io, for networks that can reach Google's
registry and not GitHub's. `--registry gcr` takes the server and UI from there instead, version
lookup included:

```sh
cub server install --registry gcr   # gcr.io/confighubapp/confighub and .../ui
```

`--registry` also takes the prefix of your own mirror, holding the two images as
`<prefix>/confighub` and `<prefix>/ui`. Re-running an install with `--registry` keeps its version
and pulls it from the registry named. Postgres (`postgres:17-alpine`) and Keycloak
(`quay.io/keycloak/keycloak`) are not copied; `--keycloak-image` names a mirror of Keycloak.

## Upgrading

```sh
cub server upgrade                          # plan the newest release; changes nothing
cub server upgrade --version v0.9.5 --apply # upgrade the running instance
```

`upgrade` reads the instance from the cluster (`--kube-context`, `--kubeconfig`, `--namespace`),
so it works however the instance was deployed, including from manifests you keep yourself, with
nothing under `~/.confighub/servers`. It moves the server, the init container that migrates its
database, and the UI to the new version's tag, and keeps each image's registry, so a mirror stays
a mirror (copy the new version into it first).

It writes one strategic-merge patch per Deployment to `--out-dir`. Merge them into the manifests
you apply (they also work as kustomize patches), or run the `kubectl patch` commands it prints.
`--apply` patches the running Deployments itself, server first, and waits for each rollout. The
manifests still need the same change, or their next apply undoes the upgrade.

## Where it runs

With nothing named, an install creates a kind cluster. Name a kubeconfig context and it installs
into the cluster you already have:

```sh
cub server install                              # creates a kind cluster
cub server install --cluster-name evaluation    # ...under a different name
cub server install --kube-context my-cluster    # a cluster you already have
```

Both render the same manifests. An evaluation on a laptop and a real deployment differ in where
they run, not in what runs.

`--cluster-name` and `--kube-context` are the two halves of that choice — one creates, the other
uses — so naming both is refused rather than resolved.

## Re-running is safe

Every generated value — the token signing key, the worker master secret, the database password,
the administrator's public key — is read back from the previous run's output and reused. Rotating
the signing key would log every session out; rotating the worker master secret would break every
worker that had enrolled. So re-running resumes rather than replacing, and a re-run after a
failed one picks up where it stopped.

```sh
cub server install --dry-run    # render everything, create nothing
```

Once an identity provider is installed, `cub server keycloak install` is the command that
re-renders the instance, and `cub server install` refuses rather than running. It would drop the
key the server authenticates to Keycloak with, and Keycloak does not re-import a realm it
already has — so that key could never be matched again.

## Install files

Files produced by an install live in one directory (`~/.confighub/servers/<name>` by
default, or `--out-dir`):

```
config/       manifests. No secrets — committable and diffable.
secrets/      the Secrets. Not committable.
kubeconfig    for the cluster this install created
kind-cluster.yaml
```

With an identity provider installed, `config/` also holds Keycloak's manifests and the realm it
imports, and `secrets/` holds a second Secret that only Keycloak reads.

One exception is the admin private key which is stored in `~/.confighub/keys` per cub CLI convention.

## Uninstalling

```sh
cub server uninstall                 # delete the cluster and the generated config
cub server uninstall --keep-config   # keep the config, so a reinstall is the same instance
```

For a kind install this deletes the cluster and everything in it, database included. For an
install into an existing cluster it deletes the namespace and leaves the cluster alone. The
private key is left behind, even if it was generated as part of the install.

## Custom generating key material

`cub server install` generates several pieces of config automatically. These can also be generated piecemeal:

```sh
cub server key admin             # the local administrator's keypair
cub server key signing           # CONFIGHUB_TOKEN_SIGNING_KEY_JWK
cub server key worker-secret     # WORKER_MASTER_SECRET
cub server key keycloak-client   # the key a server authenticates to Keycloak with
```

`key admin` splits the halves: the public one to stdout for the instance's configuration, the
private one into cub's key directory where `cub auth login --private-key` finds it.

`key keycloak-client` is for pointing a server at a Keycloak you already run, rather than the
one `keycloak install` bundles. It generates one key and prints both halves: the private one to
stdout for `KEYCLOAK_CLIENT_PRIVATE_KEY_JWK`, and the public one to stderr as the inline JWKS to
put on the Keycloak client alongside `clientAuthenticatorType: client-jwt`. Run it once — halves
from two runs do not go together, and Keycloak reports that as a signature failure rather than
as a mismatched key.
