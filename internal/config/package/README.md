# ConfigHub installer package

An [installer](https://github.com/confighub/installer) package for a self-hosted ConfigHub server.

The base holds what every install runs:
- its Namespace (Pod Security Standards restricted, enforced)
- its ServiceAccount
- the server's Deployment, at a pinned image tag, whose init container migrates the database before the server starts
- the web UI's Deployment, at the same version
- ClusterIP Services for the API, the OCI registry and the UI
- the server's environment, but for its secrets: the `confighub-env` ConfigMap
- the UI's environment: the `confighub-ui-env` ConfigMap

| Component | Adds |
|---|---|
| `bundled-postgres` (default) | PostgreSQL in the install namespace, in sync wave -1 so it is up before the server migrates it |
| `nodeport` | NodePorts for the API, the registry and the UI, and `OCI_EXTERNAL_PORT`, so the server advertises the registry where clients reach it. Conflicts with `traefik` |
| `traefik` | Traefik IngressRoutes for the API on a hostname, redirecting HTTP to HTTPS. Needs Traefik's CRDs |

A render fails validation if:
- an image is `:latest` or untagged
- a pod doesn't meet the Pod Security Standards restricted level

## Secrets

The server reads its secrets from the `confighub-secret-env` Secret in the install namespace, which is not part of the package and is never uploaded to ConfigHub. Create it yourself, or with your secret store:

| Key | Value |
|---|---|
| `CONFIGHUB_TOKEN_SIGNING_KEY_JWK` | The key the server signs tokens with: `cub server key signing` |
| `DATABASE_URL` | The database's connection string |
| `POSTGRES_PASSWORD` | With `bundled-postgres` only: its password, which `DATABASE_URL` has to embed, as `postgresql://confighub:<password>@confighub-postgres:5432/confighub?sslmode=disable` |

Keep the signing key: a new one logs everyone out.

## Install

```sh
installer setup --pull internal/config/package \
  --work-dir confighub-local --namespace confighub \
  --select bundled-postgres --select nodeport \
  --input api_url=http://localhost:32180 \
  --input ui_url=http://localhost:32183 \
  --input 'admin_jwk={"kty":"OKP","crv":"Ed25519","x":"..."}'
```

| Input | Default | Description |
|---|---|---|
| `api_url` | `confighubplaceholder` | Where a browser reaches the API. The UI calls it from the browser. |
| `ui_url` | `confighubplaceholder` | Where a browser reaches the UI. The server advertises it in `/api/info`. |
| `admin_jwk` | `confighubplaceholder` | The public JWK of the bootstrap administrator, as `cub server key admin` prints it. |
| `api_node_port` | `32180` | With `nodeport`: the API's NodePort. |
| `oci_node_port` | `32181` | With `nodeport`: the OCI registry's NodePort. |
| `ui_node_port` | `32183` | With `nodeport`: the UI's NodePort. |
| `host` | `confighubplaceholder` | With `traefik`: the hostname routed to the API. |

To run other images, such as a mirror's copies, pass `--set-image <image>=<registry>/<image>` for each of `ghcr.io/confighubai/confighub`, `ghcr.io/confighub/ui` and, with `bundled-postgres`, `postgres`. A reference without an image tag keeps the package's.

## Restarts on configuration changes

Both images read their environment once, at start, so a changed ConfigMap takes effect only when the pods restart. The pod templates carry no hash annotation of their own: uploaded to ConfigHub, each gets a `hash.confighub.com/<configmap>` annotation for each ConfigMap it reads, and its Space gets the `set-hash` Trigger that hashes the ConfigMaps, so a changed ConfigMap rolls the Deployment that reads it. This needs ConfigHub v0.10.0 or later, whose upload adds them.

## Apply order

Resources are ordered by `argocd.argoproj.io/sync-wave`, lowest first:

| Wave | Resources |
|---|---|
| -2 | The Namespace, which everything else is created in |
| -1 | The bundled database, which the server's init container migrates |
| 0 | Everything else |

Argo CD applies each wave, and waits for it to be healthy, before the next. A wave applied before the one before it is healthy, or everything applied at once, works too once the Namespace exists: the server's init container fails until Postgres answers, and succeeds when it restarts.

## Validation

`validation/` holds what the server, at the package's version, accepts and expects, generated from its image with `docgen`: its environment variables as a JSON Schema (`env.schema.json`), and its ports, paths and probes (`runtime.yaml`). `installer doc` shows them, and `installer.yaml`'s `spec.validation.howToRegenerate` regenerates them; do so with every version bump. The test checks the package against them.

## Testing

`scripts/test-package.sh` (`make test-package`) renders the package for each shape of install with `installer setup`, running its validators, and checks the renders it has to refuse. It needs installer 0.3.4 or later (or `$INSTALLER`), kustomize and yq, and no ConfigHub server. The `package` workflow runs it on every change.

The package's version is the ConfigHub release it runs, server and UI alike: package 0.10.3 runs v0.10.3.
