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
| `keycloak` | Keycloak in the install namespace, in sync wave -1, so that people sign in as themselves. The realm it imports, and the server's and the UI's settings for it. See [Keycloak](#keycloak) |
| `nodeport` | NodePorts for the API, the registry, the UI and, with `keycloak`, Keycloak, and `OCI_EXTERNAL_PORT`, so the server advertises the registry where clients reach it, unless `oci_url` says where that is. Conflicts with `traefik` |
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
| `KEYCLOAK_CLIENT_PRIVATE_KEY_JWK` | With `keycloak` only: the key the server authenticates to Keycloak with, as `cub server key keycloak-client` prints it to stdout |

With `keycloak`, Keycloak reads its own admin's password from `KEYCLOAK_BOOTSTRAP_ADMIN_PASSWORD` in a second Secret, `confighub-keycloak-secret`. It is separate because the server reads its Secret whole, and this is an administrator of every realm on that Keycloak.

Create them before applying the package, or in sync wave -2 with the Namespace: Keycloak, in wave -1, reads its Secret as it starts.

Keep the signing key: a new one logs everyone out. Keep the Keycloak client key too: Keycloak imports its public half with the realm, on its first start only, so a new one can't be matched afterwards.

## Install

```sh
installer setup --pull oci://ghcr.io/confighub/packages/confighub:0.10.4 \
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
| `oci_url` | empty | Where clients reach the OCI registry, like `https://oci.example.com`, as `OCI_EXTERNAL_URL`. The server advertises it in `/api/info`, and the token realm follows it. Empty leaves the server to work it out from each request; with `nodeport`, a URL replaces the NodePort it would advertise. Set it behind a load balancer that terminates TLS without forwarded headers, or for a registry on a name other than `oci.<API host>`. |
| `ui_node_port` | `32183` | With `nodeport`: the UI's NodePort. |
| `host` | `confighubplaceholder` | With `traefik`: the hostname routed to the API. |
| `keycloak_url` | `confighubplaceholder` | With `keycloak`: where a browser reaches Keycloak, without a trailing slash. It is the issuer of every token. |
| `keycloak_client_jwks` | `confighubplaceholder` | With `keycloak`: the public half of `KEYCLOAK_CLIENT_PRIVATE_KEY_JWK`, as `cub server key keycloak-client` prints it to stderr. |
| `keycloak_node_port` | `32182` | With `keycloak` and `nodeport`: Keycloak's NodePort. |
| `keycloak_org_domain` | `confighub.local` | With `keycloak`: the email domain of the organization users join. |
| `keycloak_ssl_required` | `none` | With `keycloak`: the realm's `sslRequired`, `none` for plain http, as on a laptop, or `external` behind TLS. |

To run other images, such as a mirror's copies, pass `--set-image <image>=<registry>/<image>` for each of `ghcr.io/confighubai/confighub`, `ghcr.io/confighub/ui`, with `bundled-postgres`, `postgres`, and with `keycloak`, `quay.io/keycloak/keycloak`. A reference without an image tag keeps the package's.

## Keycloak

The `keycloak` component runs Keycloak in development mode, with its embedded database, for evaluation; an instance serving real users wants a Keycloak of its own. It imports the `confighub` realm on its first start, and never again, so the realm's inputs reach a fresh install only. The realm has:
- the `confighub` client, which the server authenticates as by signing assertions with its key. Its service account administers the realm.
- the public `cub` client, which `cub` signs in through with the device flow
- the public `confighub-ui` client, which the UI signs in as with PKCE, returning only to `ui_url`
- the `default` organization, which every user has to be a member of
- the realm roles the server reads a user's org role from: `admin`, `manager`, `editor`, `user`, `viewer`, `creator` and `none`

One thing the realm can't say. The server reads a user's organization from the token's `organization` claim, which carries organization ids only once the mapper of Keycloak's built-in `organization` client scope has "Add organization ID" turned on, and a realm that declares a client scope replaces all of the built-in ones. Until it is turned on, a browser sign-in ends in "subject_token carries no organization". Once Keycloak has imported the realm, turn it on, with the other settings `keycloak configure` checks, as the server's client:

```sh
cub server keycloak configure \
  --keycloak-url http://localhost:32182 --ui-url http://localhost:32183 \
  --client-key-file <the file holding KEYCLOAK_CLIENT_PRIVATE_KEY_JWK>
```

It also writes patches for the server's configuration, which the package has already made; they can be ignored. `cub server keycloak install` does this step itself, rendering with this package when the instance was installed with `cub server install --installer`.

The bootstrap administrator's key keeps working, and is the way in if Keycloak is unavailable.

## Restarts on configuration changes

Both images read their environment once, at start, so a changed ConfigMap takes effect only when the pods restart. The pod templates carry no hash annotation of their own: uploaded to ConfigHub, each gets a `hash.confighub.com/<configmap>` annotation for each ConfigMap it reads, and its Space gets the `set-hash` Trigger that hashes the ConfigMaps, so a changed ConfigMap rolls the Deployment that reads it. This needs ConfigHub v0.10.0 or later, whose upload adds them.

## Apply order

Resources are ordered by `argocd.argoproj.io/sync-wave`, lowest first:

| Wave | Resources |
|---|---|
| -2 | The Namespace, which everything else is created in |
| -1 | The bundled database, which the server's init container migrates, and Keycloak, which the server reaches at startup |
| 0 | Everything else |

Argo CD applies each wave, and waits for it to be healthy, before the next. A wave applied before the one before it is healthy, or everything applied at once, works too once the Namespace exists: the server's init container fails until Postgres answers, and the server exits until Keycloak answers, and each succeeds when it restarts. Keycloak takes about a minute to import its realm on its first start.

## Validation

`validation/` holds what the server, at the package's version, accepts and expects, generated from its image with `docgen`: its environment variables as a JSON Schema (`env.schema.json`), and its ports, paths and probes (`runtime.yaml`). `installer doc` shows them, and `installer.yaml`'s `spec.validation.howToRegenerate` regenerates them; do so with every version bump. The test checks the package against them.

## Testing

`scripts/test-package.sh` (`make test-package`) renders the package for each shape of install with `installer setup`, running its validators, and checks the renders it has to refuse. It needs installer 0.3.4 or later (or `$INSTALLER`), kustomize and yq, and no ConfigHub server. The `package` workflow runs it on every change.

The package's version is the ConfigHub release it runs, server and UI alike: package 0.10.4 runs v0.10.4.

## Publishing

On every push to main, the `package` workflow publishes the package as `oci://ghcr.io/confighub/packages/confighub:<version>`, signed with cosign, unless that version is already published. So a change to the package ships when its version is bumped to the next ConfigHub release. `cub server install --installer` pulls it from there, or from a mirror its `--registry` names.
