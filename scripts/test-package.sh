#!/usr/bin/env bash
#
# Renders the installer package in internal/config/package the way an operator
# would, with `installer setup`, for each shape of install, and checks the
# renders it has to refuse. The package's validators (vet-schemas, vet-images,
# vet-pod-security-standard, ...) run in every render.
#
# Needs installer 0.3.4 or later (or $INSTALLER), kustomize and yq on PATH, and
# no ConfigHub server.
#
# Usage: scripts/test-package.sh

set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pkg="${repo}/internal/config/package"
installer="${INSTALLER:-installer}"

# On macOS, the installer on PATH may be the system's package installer.
if ! "$installer" setup --help >/dev/null 2>&1; then
  echo "${installer} is not ConfigHub's installer; set INSTALLER to its path" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

common=(--non-interactive --namespace confighub)

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# The package's version is the ConfigHub release it runs, server and UI alike.
version="$(yq -r .installerMetadata.version "${pkg}/installer.yaml")"
for image in ghcr.io/confighubai/confighub ghcr.io/confighub/ui; do
  tag="$(yq -r ".images[] | select(.name == \"${image}\") | .newTag" "${pkg}/bases/default/kustomization.yaml")"
  [ "v${version}" = "$tag" ] || fail "installerMetadata.version is ${version}, but the package runs ${image}:${tag}"
done

# render <case> <installer setup args...> renders the package.
render() {
  local name=$1
  shift
  "$installer" setup --pull "$pkg" --work-dir "${work}/${name}" "${common[@]}" "$@" \
    > "${work}/${name}.log" 2>&1
}

# manifest <case> <file>: a rendered manifest's path.
manifest() {
  echo "${work}/$1/out/manifests/$2"
}

# expect_render <case> <components> <installer setup args...>: the render
# succeeds and selects exactly those components.
expect_render() {
  local name=$1 want=$2
  shift 2
  if ! render "$name" "$@"; then
    fail "${name}: render failed"
    sed 's/^/  /' "${work}/${name}.log" >&2
    return 1
  fi
  local out="${work}/${name}/out"
  local components
  components="$(yq -r '(.spec.components // []) | join(",")' "${out}/record/selection.yaml")"
  [ "$components" = "$want" ] || fail "${name}: selected ${components}, want ${want}"
  [ -z "$(ls -A "${out}/secrets" 2>/dev/null)" ] || fail "${name}: rendered a Secret; confighub-secret-env is supplied out of band"
  check_validation "$name"
  echo "ok: ${name}"
}

# check_validation <case>: the server's rendered configuration agrees with
# what validation/ says its image accepts and expects.
check_validation() {
  local name=$1
  local deployment schema runtime
  deployment="$(manifest "$name" deployment-confighub-confighub.yaml)"
  schema="${pkg}/validation/env.schema.json"
  runtime="${pkg}/validation/runtime.yaml"
  # Every variable the package sets is one the server reads.
  local var
  for var in $(yq -r '.data | keys | .[]' "$(manifest "$name" configmap-confighub-confighub-env.yaml)") \
    $(yq -r '.spec.template.spec.containers[] | select(.name == "confighub") | (.env // [])[].name' "$deployment"); do
    [ "$(yq -r ".properties | has(\"${var}\")" "$schema")" = true ] ||
      fail "${name}: the server is given ${var}, which validation/env.schema.json doesn't list"
  done
  # Each port the server listens on is a named container port.
  local port number
  while read -r port number; do
    [ "$(yq -r ".spec.template.spec.containers[] | select(.name == \"confighub\") | .ports[] | select(.name == \"${port}\") | .containerPort" "$deployment")" = "$number" ] ||
      fail "${name}: the server's container has no port ${port} on ${number}, which validation/runtime.yaml lists"
  done < <(yq -r '.Ports[] | .Name + " " + (.Default | tostring)' "$runtime")
  # Each probe the server serves is the container's probe of that type, but
  # for readiness, which the package checks with /internal/ok.
  local type path portname
  while read -r type path portname; do
    [ "$type" != Readiness ] || path=/internal/ok
    number="$(yq -r ".Ports[] | select(.Name == \"${portname}\") | .Default" "$runtime")"
    local probe="$(echo "$type" | tr '[:upper:]' '[:lower:]')Probe"
    [ "$(yq -r ".spec.template.spec.containers[] | select(.name == \"confighub\") | .${probe}.httpGet | .path + \" \" + (.port | tostring)" "$deployment")" = "${path} ${number}" ] ||
      fail "${name}: the server's ${probe} isn't ${path} on ${number}, as validation/runtime.yaml says"
  done < <(yq -r '.Probes[] | .Type + " " + .HTTPGet.Path + " " + .HTTPGet.PortName' "$runtime")
}

# expect_refusal <case> <message> <installer setup args...>: the render fails,
# saying message.
expect_refusal() {
  local name=$1 want=$2
  shift 2
  if render "$name" "$@"; then
    fail "${name}: rendered, want a refusal saying: ${want}"
    return
  fi
  if ! grep -qF -- "$want" "${work}/${name}.log"; then
    fail "${name}: refused without saying: ${want}"
    sed 's/^/  /' "${work}/${name}.log" >&2
    return
  fi
  echo "ok: ${name}"
}

# expect_equal <case> <what> <got> <want>
expect_equal() {
  [ "$3" = "$4" ] || fail "$1: $2 is '$3', want '$4'"
}

server_env() {
  yq -r ".data.$2 // \"\"" "$(manifest "$1" configmap-confighub-confighub-env.yaml)"
}

ui_env() {
  yq -r ".data.$2 // \"\"" "$(manifest "$1" configmap-confighub-confighub-ui-env.yaml)"
}

# realm <case> <yq expression>: a field of the realm Keycloak imports.
realm() {
  yq -r '.data["confighub-realm.json"]' "$(manifest "$1" configmap-confighub-confighub-keycloak-realm.yaml)" |
    yq -p json -o yaml -r "$2"
}

node_port() {
  yq -r '.spec.type + ":" + (.spec.ports[0].nodePort // "" | tostring)' "$(manifest "$1" "service-confighub-$2.yaml")"
}

# A base, rendered with no inputs: the bundled database, ClusterIP Services,
# and placeholders for what each deployment of it sets.
if expect_render base bundled-postgres --components default; then
  expect_equal base CONFIGHUB_UI_URL "$(server_env base CONFIGHUB_UI_URL)" confighubplaceholder
  expect_equal base CONFIGHUB_URL "$(ui_env base CONFIGHUB_URL)" confighubplaceholder
  expect_equal base CONFIGHUB_LOCAL_ADMIN_JWK "$(server_env base CONFIGHUB_LOCAL_ADMIN_JWK)" confighubplaceholder
  expect_equal base "the API Service" "$(node_port base confighub-api)" ClusterIP:
  expect_equal base OCI_EXTERNAL_PORT "$(server_env base OCI_EXTERNAL_PORT)" ""
  # The Namespace comes first, then Postgres, then the server, which migrates
  # it.
  for f in namespace-confighub.yaml:-2 statefulset-confighub-confighub-postgres.yaml:-1 service-confighub-confighub-postgres.yaml:-1; do
    wave="$(yq -r '.metadata.annotations["argocd.argoproj.io/sync-wave"]' "$(manifest base "${f%%:*}")")"
    expect_equal base "${f%%:*}'s sync wave" "$wave" "${f##*:}"
  done
  # Upload adds a hash annotation per ConfigMap to a pod template only if it
  # carries no confighub.com/Hash of its own.
  for f in deployment-confighub-confighub.yaml deployment-confighub-confighub-ui.yaml; do
    hash="$(yq -r '.spec.template.metadata.annotations["confighub.com/Hash"] // ""' "$(manifest base "$f")")"
    expect_equal base "${f}'s confighub.com/Hash" "$hash" ""
  done
fi

# A local cluster's install: NodePorts, the addresses they make, and the
# administrator's key, whose JSON reaches the ConfigMap as given.
jwk='{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}'
if expect_render nodeport bundled-postgres,nodeport --select bundled-postgres --select nodeport \
  --input api_url=http://localhost:31080 --input ui_url=http://localhost:31083 --input "admin_jwk=${jwk}" \
  --input api_node_port=31080 --input oci_node_port=31081 --input ui_node_port=31083; then
  expect_equal nodeport "the API Service" "$(node_port nodeport confighub-api)" NodePort:31080
  expect_equal nodeport "the registry's Service" "$(node_port nodeport confighub-oci-server)" NodePort:31081
  expect_equal nodeport "the UI Service" "$(node_port nodeport confighub-ui)" NodePort:31083
  expect_equal nodeport OCI_EXTERNAL_PORT "$(server_env nodeport OCI_EXTERNAL_PORT)" 31081
  expect_equal nodeport CONFIGHUB_UI_URL "$(server_env nodeport CONFIGHUB_UI_URL)" http://localhost:31083
  expect_equal nodeport CONFIGHUB_URL "$(ui_env nodeport CONFIGHUB_URL)" http://localhost:31080
  expect_equal nodeport CONFIGHUB_LOCAL_ADMIN_JWK "$(server_env nodeport CONFIGHUB_LOCAL_ADMIN_JWK)" "$jwk"
  [ ! -e "$(manifest nodeport service-confighub-confighub-keycloak.yaml)" ] ||
    fail "nodeport: rendered Keycloak, which wasn't selected"
fi

# Behind Traefik, on a hostname.
if expect_render traefik bundled-postgres,traefik --select bundled-postgres --select traefik \
  --input host=confighub.example.com; then
  match="$(yq -r '.spec.routes[0].match' "$(manifest traefik ingressroute-confighub-confighub-api-https.yaml)")"
  expect_equal traefik "the API route" "$match" 'Host(`confighub.example.com`) && !PathPrefix(`/internal`)'
  match="$(yq -r '.spec.routes[0].match' "$(manifest traefik ingressroute-confighub-confighub-redirect-http.yaml)")"
  expect_equal traefik "the redirect route" "$match" 'Host(`confighub.example.com`)'
fi

# With Keycloak, on a local cluster: the server's and the UI's settings for it,
# its NodePort, and the realm it imports.
jwks='{"keys":[{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo","kid":"k1","use":"sig","alg":"EdDSA"}]}'
if expect_render keycloak bundled-postgres,keycloak,nodeport --select bundled-postgres --select keycloak --select nodeport \
  --input ui_url=http://localhost:31083 --input keycloak_url=http://localhost:31082 \
  --input "keycloak_client_jwks=${jwks}" --input keycloak_node_port=31082 --input keycloak_org_domain=example.com; then
  expect_equal keycloak KEYCLOAK_AUTH_URL "$(server_env keycloak KEYCLOAK_AUTH_URL)" http://localhost:31082
  expect_equal keycloak CONFIGHUB_AUTH_ISSUER "$(server_env keycloak CONFIGHUB_AUTH_ISSUER)" http://localhost:31082/realms/confighub
  expect_equal keycloak KEYCLOAK_INTERNAL_URL "$(server_env keycloak KEYCLOAK_INTERNAL_URL)" http://confighub-keycloak:8080
  expect_equal keycloak CONFIGHUB_UI_OAUTH_CLIENT_ID "$(ui_env keycloak CONFIGHUB_UI_OAUTH_CLIENT_ID)" confighub-ui
  expect_equal keycloak "Keycloak's Service" "$(node_port keycloak confighub-keycloak)" NodePort:31082
  hostname="$(yq -r '.spec.template.spec.containers[] | select(.name == "keycloak") | .env[] | select(.name == "KC_HOSTNAME") | .value' \
    "$(manifest keycloak statefulset-confighub-confighub-keycloak.yaml)")"
  expect_equal keycloak KC_HOSTNAME "$hostname" http://localhost:31082
  expect_equal keycloak "the server client's JWKS" "$(realm keycloak '.clients[] | select(.clientId == "confighub") | .attributes["jwks.string"]')" "$jwks"
  expect_equal keycloak "the UI client's redirect URI" "$(realm keycloak '.clients[] | select(.clientId == "confighub-ui") | .redirectUris[0]')" http://localhost:31083/
  expect_equal keycloak "the UI client's web origin" "$(realm keycloak '.clients[] | select(.clientId == "confighub-ui") | .webOrigins[0]')" http://localhost:31083
  expect_equal keycloak "the organization's domain" "$(realm keycloak '.organizations[0].domains[0].name')" example.com
  expect_equal keycloak sslRequired "$(realm keycloak .sslRequired)" none
  expect_equal keycloak "the org roles" "$(realm keycloak '[.roles.realm[].name] | join(",")')" admin,manager,editor,user,viewer,creator,none
  # Keycloak is serving before the server, which reaches it at startup.
  for f in configmap-confighub-confighub-keycloak-realm.yaml service-confighub-confighub-keycloak.yaml statefulset-confighub-confighub-keycloak.yaml; do
    wave="$(yq -r '.metadata.annotations["argocd.argoproj.io/sync-wave"]' "$(manifest keycloak "$f")")"
    expect_equal keycloak "${f}'s sync wave" "$wave" -1
  done
fi

# Keycloak with no inputs: placeholders for what each deployment of it sets,
# in a realm that is still JSON, and a ClusterIP Service.
if expect_render keycloak-defaults bundled-postgres,keycloak --select bundled-postgres --select keycloak; then
  expect_equal keycloak-defaults KEYCLOAK_AUTH_URL "$(server_env keycloak-defaults KEYCLOAK_AUTH_URL)" confighubplaceholder
  expect_equal keycloak-defaults CONFIGHUB_AUTH_ISSUER "$(server_env keycloak-defaults CONFIGHUB_AUTH_ISSUER)" confighubplaceholder
  expect_equal keycloak-defaults "Keycloak's Service" "$(node_port keycloak-defaults confighub-keycloak)" ClusterIP:
  expect_equal keycloak-defaults "the server client's JWKS" "$(realm keycloak-defaults '.clients[] | select(.clientId == "confighub") | .attributes["jwks.string"]')" confighubplaceholder
  expect_equal keycloak-defaults "the UI client's redirect URI" "$(realm keycloak-defaults '.clients[] | select(.clientId == "confighub-ui") | .redirectUris[0]')" confighubplaceholder
  expect_equal keycloak-defaults "the organization's domain" "$(realm keycloak-defaults '.organizations[0].domains[0].name')" confighub.local
fi

# A database run elsewhere: nothing of Postgres is rendered.
if expect_render external-database "" --components minimal; then
  [ ! -e "$(manifest external-database statefulset-confighub-confighub-postgres.yaml)" ] ||
    fail "external-database: rendered the bundled Postgres"
fi

# From a mirror: every image, the bundled database's and Keycloak's included. A
# reference without an image tag keeps the package's.
if expect_render mirror bundled-postgres,keycloak --select bundled-postgres --select keycloak \
  --set-image ghcr.io/confighubai/confighub=mirror.example.com/confighub \
  --set-image ghcr.io/confighub/ui=mirror.example.com/ui \
  --set-image postgres=mirror.example.com/postgres \
  --set-image quay.io/keycloak/keycloak=mirror.example.com/keycloak; then
  images="$(yq -N -r '.spec.template.spec | (.initContainers // []) + .containers | .[].image' "${work}"/mirror/out/manifests/{deployment,statefulset}-*.yaml | sort -u)"
  while read -r image; do
    case "$image" in
    mirror.example.com/*:*) ;;
    *) fail "mirror: ${image} is not from the mirror, with an image tag" ;;
    esac
  done <<< "$images"
fi

expect_refusal nodeport-and-traefik 'components "nodeport" and "traefik" conflict' --select nodeport --select traefik
expect_refusal latest-image vet-images \
  --set-image ghcr.io/confighubai/confighub=ghcr.io/confighubai/confighub:latest

if [ "$failures" -gt 0 ]; then
  echo "${failures} check(s) failed" >&2
  exit 1
fi
echo "All package checks passed."
