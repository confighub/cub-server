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

## Install files

Files produced by an install live in one directory (`~/.confighub/servers/<name>` by
default, or `--out-dir`):

```
config/       manifests. No secrets — committable and diffable.
secrets/      the Secret. Not committable.
kubeconfig    for the cluster this install created
kind-cluster.yaml
```

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
cub server key admin           # the local administrator's keypair
cub server key signing         # JWT_PRIVATE_KEY_JWK
cub server key worker-secret   # WORKER_MASTER_SECRET
```

`key admin` splits the halves: the public one to stdout for the instance's configuration, the
private one into cub's key directory where `cub auth login --private-key` finds it.
