# cub-server

`cub server` installs a self-hosted ConfigHub Server instance in your infrastructure.

The simplest usage:

```sh
cub plugin install confighub/cub-server
cub server install -i
```

This creates a local Kubernetes cluster (using the kind Go library), deploys ConfigHub and
a database into it, waits for the instance to answer, and signs you in. Once complete,  you can immediately start using ConfigHub CLI, e.g:

```
cub space list
```

You can also log into the Web UI by using the authenticated CLI to start a browser session:

```
cub auth browser-session
```

## Why there is no identity provider to set up

ConfigHub can run with no IdP configured. The instance is created with a single local
administrator, and `cub server install` generates that administrator's keypair as part of the
install: the public half goes into the instance's configuration, and the private half is written
to cub's key directory, where `cub auth login --private-key` finds it.

So there is no Keycloak to stand up, no realm to import, no redirect URI to have registered by
somebody with admin, and no password anywhere. What the instance stores is a public key.

## Prerequisites

- **Docker**, running.
- **kubectl** on your PATH.

The cluster is created through kind's Go API, so the `kind` binary is not needed.

Installing into a cluster you already have needs only `kubectl` and a kubeconfig.

The ConfigHub server image is public on ghcr.io, so no registry credentials are needed.

## What it installs

Into a namespace (`confighub` by default):

| | |
|---|---|
| ConfigHub | API and UI on a NodePort, OCI registry on another |
| PostgreSQL | bundled by default; `--database=external` points at your own |

The API is published on a real host port rather than through `kubectl port-forward`, so the URL
keeps working after you close the terminal. That is why the NodePorts are fixed defaults: kind
has to publish them when the node container is created, before there is a cluster to ask.

## Targets

```sh
cub server install                                          # create a kind cluster
cub server install --target=context --kube-context=my-cluster   # use one you have
```

Both render the same manifests. An evaluation on a laptop and a real deployment differ in where
they run, not in what runs.

## Re-running is safe

Every generated value — the token signing key, the worker master secret, the database password,
the administrator's public key — is read back from the previous run's output and reused. Rotating
the signing key would log every session out; rotating the worker master secret would break every
worker that had enrolled. So re-running resumes rather than replacing, and a re-run after a
failed one picks up where it stopped.

```sh
cub server install --dry-run    # render everything, create nothing
```

## Where things are

Everything one install produced lives in one directory (`~/.confighub/servers/<name>` by
default, or `--out-dir`):

```
config/       manifests. No secrets — committable and diffable.
secrets/      the Secret. Not committable.
kubeconfig    for the cluster this install created
kind-cluster.yaml
```

The administrator's private key is **not** there. It goes to cub's key directory, because it is a
credential rather than deployment state, and because it must never reach the cluster — that is
what makes the public half safe to sit in a ConfigMap.

## Uninstalling

```sh
cub server uninstall                 # delete the cluster and the generated config
cub server uninstall --keep-config   # keep the config, so a reinstall is the same instance
```

For a kind install this deletes the cluster and everything in it, database included. For an
install into an existing cluster it deletes the namespace and leaves the cluster alone. Your
private key is never touched.

## Development

```sh
make build          # bin/cub-server
make plugin         # install this build into cub, through cub's own installer
make check          # fmt-check + vet + test
```

`make plugin` goes through `cub plugin install ./bin/cub-server` rather than dropping a binary
into the plugin directory, so local development exercises the same path a release does —
including the install hook that writes `cub-plugin.yaml`.

It depends only on released modules, so a clean checkout builds.

## Generating credentials on their own

`cub server install` generates all three of these itself. These exist for the cases that are not
a fresh install — bringing your own configuration, rotating one value, or generating in one place
and deploying from another. Each writes one value to stdout, so it can be piped:

```sh
cub server key admin           # the local administrator's keypair
cub server key signing         # JWT_PRIVATE_KEY_JWK
cub server key worker-secret   # WORKER_MASTER_SECRET
```

`key admin` splits the halves: the public one to stdout for the instance's configuration, the
private one into cub's key directory where `cub auth login --private-key` finds it.
