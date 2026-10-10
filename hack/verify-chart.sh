#!/usr/bin/env bash
# Render assertions for helm/giantswarm-repo-manager: rules the templates
# encode that `helm lint` and the values schema cannot check. Runs in the chart
# workflow (.github/workflows/chart.yml) on every change and as
# `make helm-verify`, after `make helm-deps` pulled the pinned dependencies.
# Needs helm and yq (mikefarah, v4; preinstalled on the GitHub runners).
set -euo pipefail
cd "$(dirname "$0")/.."
CHART=helm/giantswarm-repo-manager

fail() { echo "verify-chart: FAIL: $*" >&2; exit 1; }

# The helm.sh/chart label is a valid label value (at most 63 characters,
# alphanumeric at both ends) for any chart version: the 63-character cut of
# "<name>-<version>" for a long version (a branch build's
# <version>-dev.<branch>.<date>.<time>.<sha>, or the <version>+<digest>
# helm-controller installs) can land on ".", on "_" (from "+") or on a run
# like "--.". Each version renders every object of the chart with the label it
# names; the valkey dependency's objects carry its own label, valid too. The
# version is set by packaging, since `helm template --version` does not apply
# to a chart directory.
pkg=$(mktemp -d)
trap 'rm -rf "$pkg"' EXIT
label_re='^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$'
while read -r v want; do
  helm package "$CHART" --version "$v" -d "$pkg" >/dev/null
  labels=$(helm template t "$pkg/$(basename "$CHART")-$v.tgz" | sed -n 's/^ *helm\.sh\/chart: *//p' | tr -d '"' | sort -u)
  [ -n "$labels" ] || fail "version $v renders no helm.sh/chart label"
  while IFS= read -r l; do
    [[ ${#l} -le 63 && $l =~ $label_re ]] || fail "version $v renders helm.sh/chart '$l', not a valid label value"
  done <<<"$labels"
  own=$(grep "^$(basename "$CHART")-" <<<"$labels" || true)
  [ "$own" = "$want" ] || fail "version $v renders helm.sh/chart '$own', want '$want'"
done <<'VERSIONS'
0.1.0 giantswarm-repo-manager-0.1.0
0.10.12-dev.renova.2026-09-22.14-54-24.h1a2b3c4 giantswarm-repo-manager-0.10.12-dev.renova.2026-09-22.14-54-24
0.10.12-dev.renova.2026-09-22.14-54-24+h1a2b3c4 giantswarm-repo-manager-0.10.12-dev.renova.2026-09-22.14-54-24
0.10.12-dev.renova.2026-09-22.14-54---.h1a2b3c4 giantswarm-repo-manager-0.10.12-dev.renova.2026-09-22.14-54
VERSIONS

# The Valkey store with default values passes the restricted Pod Security
# Standard and keeps its data on a volume: the pod runs under seccomp
# RuntimeDefault, every container and init container carries the restricted
# security context (the valkey chart applies valkey.valkey.securityContext to
# its init container too, the exporter has its own), and the data volume is a
# 1Gi ReadWriteOnce PersistentVolumeClaim of the render, not an emptyDir.
render=$(helm template t "$CHART")
pod=$(yq 'select(.kind == "Deployment" and .metadata.labels["app.kubernetes.io/name"] == "valkey") | .spec.template.spec' <<<"$render")
[ -n "$pod" ] || fail "the default render has no Valkey Deployment"
[ "$(yq '.securityContext.seccompProfile.type' <<<"$pod")" = RuntimeDefault ] ||
  fail "Valkey pod: securityContext.seccompProfile.type is not RuntimeDefault"
containers=$(yq '((.initContainers // []) + .containers)[].name' <<<"$pod")
[ -n "$containers" ] || fail "Valkey pod: no containers"
while IFS= read -r c; do
  sc=$(yq "((.initContainers // []) + .containers)[] | select(.name == \"$c\") | .securityContext" <<<"$pod")
  for rule in \
    '.seccompProfile.type == "RuntimeDefault"' \
    '.allowPrivilegeEscalation == false' \
    '(.capabilities.drop // []) | contains(["ALL"])' \
    '.readOnlyRootFilesystem == true' \
    '.runAsNonRoot == true'; do
    [ "$(yq "$rule" <<<"$sc")" = true ] || fail "Valkey container $c: securityContext $rule does not hold"
  done
done <<<"$containers"
read -r vol claim < <(yq '.volumes[] | select(.persistentVolumeClaim) | .name + " " + .persistentVolumeClaim.claimName' <<<"$pod") ||
  fail "Valkey pod: the data is on no PersistentVolumeClaim"
[ "$(yq "[.containers[] | (.volumeMounts // [])[] | select(.name == \"$vol\")] | length" <<<"$pod")" -gt 0 ] ||
  fail "Valkey pod: no container mounts the PersistentVolumeClaim $claim"
pvc=$(yq "select(.kind == \"PersistentVolumeClaim\" and .metadata.name == \"$claim\") | .spec" <<<"$render")
[ -n "$pvc" ] || fail "the default render has no PersistentVolumeClaim $claim"
[ "$(yq '(.accessModes | length) == 1 and .accessModes[0] == "ReadWriteOnce"' <<<"$pvc")" = true ] ||
  fail "PersistentVolumeClaim $claim: accessModes $(yq -o=json -I=0 '.accessModes' <<<"$pvc"), want [\"ReadWriteOnce\"]"
[ "$(yq '.resources.requests.storage' <<<"$pvc")" = 1Gi ] ||
  fail "PersistentVolumeClaim $claim: requests $(yq '.resources.requests.storage' <<<"$pvc"), want 1Gi"

# A rollout keeps the MCP backend reachable with default values: one
# replica, replaced by surging (maxSurge 1, maxUnavailable 0) so the old pod
# stops only once the new one is ready; the readiness probe asks /readyz on
# the serving port; and the server drains for SHUTDOWN_DELAY after SIGTERM, a
# delay plus the 15 s graceful shutdown within the termination grace period.
dep=$(yq 'select(.kind == "Deployment" and .metadata.labels["app.kubernetes.io/name"] == "giantswarm-repo-manager") | .spec' <<<"$render")
[ -n "$dep" ] || fail "the default render has no giantswarm-repo-manager Deployment"
[ "$(yq '.replicas' <<<"$dep")" = 1 ] || fail "Deployment: replicas $(yq '.replicas' <<<"$dep"), want 1"
[ "$(yq '.strategy.type == "RollingUpdate" and .strategy.rollingUpdate.maxSurge == 1 and .strategy.rollingUpdate.maxUnavailable == 0' <<<"$dep")" = true ] ||
  fail "Deployment: strategy $(yq -o=json -I=0 '.strategy' <<<"$dep"), want RollingUpdate with maxSurge 1, maxUnavailable 0"
# Old ReplicaSets go with the next rollouts: each keeps its own policy report
# results, so a revision history above 3 keeps a fixed failure reported.
[ "$(yq '.revisionHistoryLimit' <<<"$dep")" -le 3 ] 2>/dev/null ||
  fail "Deployment: revisionHistoryLimit $(yq '.revisionHistoryLimit' <<<"$dep"), want at most 3"
ctr=$(yq '.template.spec.containers[] | select(.name == "giantswarm-repo-manager")' <<<"$dep")
[ "$(yq '.readinessProbe.httpGet.path == "/readyz" and .readinessProbe.httpGet.port == "http"' <<<"$ctr")" = true ] ||
  fail "Deployment: readinessProbe $(yq -o=json -I=0 '.readinessProbe' <<<"$ctr"), want GET /readyz on port http"
delay=$(yq '.env[] | select(.name == "SHUTDOWN_DELAY") | .value' <<<"$ctr")
[[ $delay =~ ^([0-9]+)s$ ]] || fail "Deployment: SHUTDOWN_DELAY '$delay', want whole seconds"
grace=$(yq '.template.spec.terminationGracePeriodSeconds // 30' <<<"$dep")
((BASH_REMATCH[1] + 15 < grace)) || fail "Deployment: SHUTDOWN_DELAY $delay plus the 15 s shutdown exceeds the ${grace}s termination grace period"

echo "verify-chart: ok"
