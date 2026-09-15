# ConfigHub self-hosted server installation plugin

> [!IMPORTANT] 
> **You probably don't need this.** To try ConfigHub, [sign
> up](https://auth.confighub.com/sign-up) for hub.confighub.com and follow the
> [tutorial](https://docs.confighub.com/get-started/tutorial/setup/). Nothing in this repository
> is required.
>
> This plugin installs a **self-hosted** ConfigHub server. It is for prospects, customers
> and design partners with an existing ConfigHub engagement who are evaluating self-hosting.
> Use is conditioned on the [evaluation license](EVALUATION-LICENSE.txt).

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

## What it installs

Into a namespace (`confighub` by default):

| | |
|---|---|
| ConfigHub | API and UI on a NodePort, OCI registry on another |
| PostgreSQL | bundled by default; `--database=external` points at your own |
| Keycloak | not installed by default — see [Adding people](#adding-people) |

The API is published on a real host port rather than through `kubectl port-forward`, so the URL
keeps working after you close the terminal.

Ports are chosen for you. kind has to publish them when the node container is created, before
there is a cluster to ask, so an install picks free ones from the operating system rather than
insisting on a default — a second instance, or anything else already holding the port, moves it
along instead of failing. Re-running an install keeps the ports the instance already answers on;
`--node-port`, `--oci-node-port` and `--keycloak-node-port` override both.

## Adding people

`cub server install` leaves you with an instance and one administrator, who signs in with a
key (located in ~/.confighub/keys). That is enough to evaluate ConfigHub and enough to run it alone. To support multiple users, ConfigHub relies on keycloak as an IdP and SSO broker. Install it with:

```sh
cub server keycloak install
```

Keycloak is deployed alongside the instance, a realm is imported with the two clients ConfigHub
needs, and the server is reconfigured and restarted to use it. After installation you can log into keycloak's admin interface with:

```sh
cub server keycloak open
```

This will copy the kcadmin password to the clipboard and open your browser on the web interface. Login with user `kcadmin` and the password in the clipboard.

**This is additive.** Your administrator user and key keep working afterwards. It can be used for break-glass operations when there are issues with the keycloak setup that prevents other users from logging in:

```sh
cub auth login --private-key confighub-admin --server http://localhost:32180
cub auth browser-session
```

While you can use keycloak to manage users directly

### Connecting your identity provider

```sh
cub server keycloak idp -i
```

This asks for your organization's name and email domain, then for your provider's OpenID
discovery URL and the client id and secret of an application registered for ConfigHub at their
end. Okta, Entra ID, Google Workspace and Auth0 all publish a discovery document, so one URL
replaces naming each endpoint. It finishes by printing a redirect URI to register with your
provider — sign-in fails on the way back without it.

The email domain is not cosmetic. ConfigHub resolves a user's organization from their token and
shows anyone who belongs to none a pending-approval page, and the domain is what makes someone
arriving through your provider a member. That is why the organization and the provider are set
up by one command rather than two: they only produce a working login together.

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

## Which server version

`cub server install` asks the registry for the newest released ConfigHub version and installs
that, so this plugin does not have to be re-released every time the server is.

The version it resolved is written into the generated manifests, not a floating tag — so what is
running is answerable, and two people installing at the same moment get the same thing.

```sh
cub server install --image ghcr.io/confighubai/confighub:v0.4.20   # pick one
```

Re-running an install keeps the version the instance is already on; it resumes rather than
upgrading. Pass `--image` to move it, which is also how you install without reaching the
registry at all.

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
cub server key signing           # JWT_PRIVATE_KEY_JWK
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
