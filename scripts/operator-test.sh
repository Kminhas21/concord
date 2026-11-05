#!/usr/bin/env bash
# operator-test: the end-to-end acceptance gate for the concord operator. Boots a
# kind cluster, loads the locally-built concord + operator images, installs the
# operator chart, applies a TeamCoordinator in a team namespace, and proves the
# unit it builds is real: Ready=True, an authenticated RPC (bad token rejected,
# stale edit BLOCKED, matching edit ALLOWED), and that deleting the CR removes
# every <cr>-* object. Always tears the cluster down on exit.
#
# Needs: docker, kind, helm, kubectl. Local/opt-in like chart-test, not on CI.
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH 2>/dev/null)/bin:$PATH" # kind lives in Go's bin on this setup

CLUSTER=concord-operator-test
CTX=kind-$CLUSTER
NS=team-a
CR=alpha
CONCORD_IMAGE=ghcr.io/kminhas21/concord:dev
OPERATOR_IMAGE=ghcr.io/kminhas21/concord-operator:dev
READY_TIMEOUT=${READY_TIMEOUT:-240s}
k() { kubectl --context "$CTX" "$@"; }

PF_PID=""
cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" >/dev/null 2>&1 || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> build both images and create kind cluster ($CLUSTER)"
docker build -q -f deploy/docker/Dockerfile -t "$CONCORD_IMAGE" . >/dev/null
docker build -q -f deploy/docker/Dockerfile --target operator -t "$OPERATOR_IMAGE" . >/dev/null
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" >/dev/null
kind load docker-image "$CONCORD_IMAGE" "$OPERATOR_IMAGE" --name "$CLUSTER" >/dev/null

echo "==> helm install the operator chart"
helm install concord-operator deploy/helm/concord-operator --kube-context "$CTX" \
  --namespace concord-system --create-namespace \
  --set image.tag=dev --set concordImage="$CONCORD_IMAGE" --wait --timeout 150s >/dev/null

echo "==> apply a TeamCoordinator in $NS and wait Ready=True"
k create namespace "$NS" >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: concord.kminhas21.github.io/v1alpha1
kind: TeamCoordinator
metadata:
  name: $CR
  namespace: $NS
spec:
  repo: example/repo
YAML
k -n "$NS" wait --for=condition=Ready "teamcoordinator/$CR" --timeout="$READY_TIMEOUT" >/dev/null \
  || { k -n concord-system logs deploy/concord-operator-concord-operator --tail=30 || true
       k -n "$NS" get tc,all || true
       echo "FAIL: TeamCoordinator never became Ready"; exit 1; }

TOKEN=$(k -n "$NS" get secret "$CR-token" -o jsonpath='{.data.token}' | base64 -d)
[ -n "$TOKEN" ] || { echo "FAIL: empty $CR-token"; exit 1; }

echo "==> port-forward svc/$CR-concord"
k -n "$NS" port-forward "svc/$CR-concord" 8090:8080 >/tmp/concord-operator-pf.log 2>&1 &
PF_PID=$!

RPC_URL=http://localhost:8090
source scripts/lib/rpc.sh
assert_auth_rejects "$TOKEN"
assert_version_check "$TOKEN"

echo -n "==> delete the CR removes every $CR-* object: "
kill "$PF_PID" >/dev/null 2>&1 || true; PF_PID=""
# list_children: names of every $CR-* object. A kubectl failure returns non-zero
# (set -e is off inside $(...), so callers must check) instead of reading as
# "none left"; only grep's no-match is tolerated.
list_children() {
  local out
  out=$(k -n "$NS" get deploy,statefulset,svc,secret,networkpolicy -o name) || return 1
  grep "/$CR-" <<<"$out" || true
}
# Non-vacuous: the children must exist before the delete, or "gone" proves nothing.
CHILD_RE=${CHILD_RE:-"deployment.apps/$CR-concord|statefulset.apps/$CR-dragonfly|service/$CR-concord|service/$CR-dragonfly|networkpolicy.networking.k8s.io/$CR-dragonfly|secret/$CR-token"}
before=$(list_children) || fail x "kubectl failed listing children before delete"
for want in ${CHILD_RE//|/ }; do
  grep -q "$want" <<<"$before" || fail x "expected child $want before delete; have: $before"
done
k -n "$NS" delete teamcoordinator "$CR" --timeout=60s >/dev/null
left=""
for _ in $(seq 1 60); do
  left=$(list_children) || fail x "kubectl failed listing children after delete"
  [ -z "$left" ] && break
  sleep 1
done
[ -z "$left" ] || fail x "still present: $left"
echo "ok"

echo ""
echo "operator-test PASSED: CR became Ready, bad token REJECTED, stale edit BLOCKED, matching edit ALLOWED, delete removed all $CR-* objects"
