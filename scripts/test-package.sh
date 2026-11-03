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
  # Postgres comes up a sync wave before the server, which migrates it.
  for f in statefulset-confighub-confighub-postgres.yaml service-confighub-confighub-postgres.yaml; do
    wave="$(yq -r '.metadata.annotations["argocd.argoproj.io/sync-wave"]' "$(manifest base "$f")")"
    expect_equal base "${f}'s sync wave" "$wave" -1
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
fi

# Behind Traefik, on a hostname.
if expect_render traefik bundled-postgres,traefik --select bundled-postgres --select traefik \
  --input host=confighub.example.com; then
  match="$(yq -r '.spec.routes[0].match' "$(manifest traefik ingressroute-confighub-confighub-api-https.yaml)")"
  expect_equal traefik "the API route" "$match" 'Host(`confighub.example.com`) && !PathPrefix(`/internal`)'
  match="$(yq -r '.spec.routes[0].match' "$(manifest traefik ingressroute-confighub-confighub-redirect-http.yaml)")"
  expect_equal traefik "the redirect route" "$match" 'Host(`confighub.example.com`)'
fi

# A database run elsewhere: nothing of Postgres is rendered.
if expect_render external-database "" --components minimal; then
  [ ! -e "$(manifest external-database statefulset-confighub-confighub-postgres.yaml)" ] ||
    fail "external-database: rendered the bundled Postgres"
fi

# From a mirror: every image, the bundled database's included. A reference
# without an image tag keeps the package's.
if expect_render mirror bundled-postgres --components default \
  --set-image ghcr.io/confighubai/confighub=mirror.example.com/confighub \
  --set-image ghcr.io/confighub/ui=mirror.example.com/ui \
  --set-image postgres=mirror.example.com/postgres; then
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
