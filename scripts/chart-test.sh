#!/usr/bin/env bash
# chart-test: the end-to-end acceptance gate for the concord Helm chart (ticket
# 05). It boots a real kind cluster, loads the locally-built concord image into
# it (no concord registry needed; the cluster pulls the upstream Dragonfly image),
# installs the chart, and drives the version-check guarantee through concord's RPC
# seam against the in-cluster concord + Dragonfly — proving the packaged unit
# genuinely coordinates, not just that YAML renders. Always tears the cluster
# down on exit.
#
# Needs: docker, kind, helm, kubectl. Not on the CI matrix (a cluster is too
# heavy for per-push); this is the local acceptance gate, the kind analog of
# `make obs-smoke`.
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH 2>/dev/null)/bin:$PATH" # kind lives in Go's bin on this setup

CLUSTER=concord-chart-test
CTX=kind-$CLUSTER
RELEASE=cu
IMAGE=ghcr.io/kminhas21/concord:dev

PF_PID=""
cleanup() {
  [ -n "$PF_PID" ] && kill "$PF_PID" >/dev/null 2>&1 || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> create kind cluster ($CLUSTER)"
kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true # idempotent: clear a leftover from an interrupted run
kind create cluster --name "$CLUSTER" >/dev/null

# Load OUR concord image into kind so no concord registry is needed. Dragonfly
# is an upstream multi-arch public image; the cluster pulls it (kind can't load a
# manifest-list image). The only external dependency is that public pull.
echo "==> build + load the concord image into kind"
docker build -q -f deploy/docker/Dockerfile -t "$IMAGE" . >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null

# Team token: random per run, never echoed. Secret created idempotently.
TOKEN=$(head -c16 /dev/urandom | od -An -tx1 | tr -d ' \n')
kubectl --context "$CTX" create secret generic cu-token --from-literal=token="$TOKEN"   --dry-run=client -o yaml | kubectl --context "$CTX" apply -f - >/dev/null

echo "==> helm install"
helm install "$RELEASE" deploy/helm/concord --kube-context "$CTX" --set image.tag=dev   --set concord.auth.existingSecret=cu-token --wait --timeout 150s >/dev/null

echo "==> wait for rollout"
kubectl --context "$CTX" rollout status "deploy/$RELEASE-concord" --timeout=120s
kubectl --context "$CTX" rollout status "statefulset/$RELEASE-concord-dragonfly" --timeout=120s

echo "==> port-forward the concord RPC service"
kubectl --context "$CTX" port-forward "svc/$RELEASE-concord" 8080:8080 >/tmp/concord-chart-pf.log 2>&1 &
PF_PID=$!

RPC=http://localhost:8080/concord.v1.CoordinationService
# call METHOD BODY [TOKEN]: POST a Connect JSON call, with a bearer token if given.
call() {
  local auth=()
  [ -n "${3:-}" ] && auth=(-H "Authorization: Bearer $3")
  curl -s -X POST "$RPC/$1" -H 'Content-Type: application/json' ${auth[@]+"${auth[@]}"} -d "$2"
}
fail() { echo "FAIL"; shift; for l in "$@"; do echo "  $l"; done; exit 1; }

# Auth: a missing or wrong token is rejected in-cluster. Retry to absorb
# port-forward + rollout readiness (the obs-smoke pattern).
echo -n "==> auth rejects a missing and a wrong token: "
none="" wrong="" ok=""
for _ in $(seq 1 30); do
  none=$(call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' || true)
  wrong=$(call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' "not-$TOKEN" || true)
  if echo "$none" | grep -q unauthenticated && echo "$wrong" | grep -q unauthenticated; then
    ok=1
    break
  fi
  sleep 1
done
[ -n "$ok" ] || fail x "no-token response: $none" "wrong-token response: $wrong"
echo "ok"

# Drive the version check with the right token: record a read at v1, then a
# stale edit (v2) must BLOCK and a matching edit (v1) must ALLOW.
echo -n "==> drive version check with the good token (stale blocks, matching allows): "
stale="" allow="" ok=""
for _ in $(seq 1 30); do
  if call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' "$TOKEN" | grep -vq unauthenticated; then
    # `|| true` so a port-forward flap between calls retries instead of
    # aborting under `set -e`.
    stale=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v2"}' "$TOKEN" || true)
    allow=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v1"}' "$TOKEN" || true)
    if echo "$stale" | grep -q 'changed since you last read it'       && echo "$allow" | grep -q '"allowed":true'; then
      ok=1
      break
    fi
  fi
  sleep 1
done
[ -n "$ok" ] || fail x "stale response: $stale" "allow response: $allow"
echo "ok"

# Readiness tracks Dragonfly: 0 replicas -> concord pod NotReady; back to 1 -> Ready.
SEL="app.kubernetes.io/instance=$RELEASE,app.kubernetes.io/component=coordination"
echo -n "==> readiness: Dragonfly down -> concord NotReady, up -> Ready: "
kubectl --context "$CTX" scale "statefulset/$RELEASE-concord-dragonfly" --replicas=0 >/dev/null
kubectl --context "$CTX" wait --for=condition=Ready=false pod -l "$SEL" --timeout=90s >/dev/null   || fail x "concord pod never went NotReady with Dragonfly at 0 replicas"
kubectl --context "$CTX" scale "statefulset/$RELEASE-concord-dragonfly" --replicas=1 >/dev/null
kubectl --context "$CTX" wait --for=condition=Ready pod -l "$SEL" --timeout=150s >/dev/null   || fail x "concord pod never returned to Ready after Dragonfly came back"
echo "ok"

echo ""
echo "chart-test PASSED: in-cluster concord + Dragonfly — bad token REJECTED, stale edit BLOCKED, matching edit ALLOWED, readiness tracks Dragonfly"
