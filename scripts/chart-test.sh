#!/usr/bin/env bash
# chart-test: the end-to-end acceptance gate for the concord Helm chart (ticket
# 05). It boots a real kind cluster, loads the locally-built image (no registry),
# installs the chart, and drives the version-check guarantee through concord's
# RPC seam against the in-cluster concord + Dragonfly — proving the packaged unit
# genuinely coordinates, not just that YAML renders. Always tears the cluster
# down on exit.
#
# Needs: docker, kind, helm, kubectl. Not on the CI matrix (a cluster is too
# heavy for per-push); this is the local acceptance gate, the kind analog of
# `make obs-smoke`.
set -euo pipefail

cd "$(dirname "$0")/.."
export PATH="$(go env GOPATH)/bin:$PATH" # kind lives in Go's bin on this setup

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
kind create cluster --name "$CLUSTER" >/dev/null

echo "==> build + load the local image (offline — no registry)"
docker build -q -f deploy/docker/Dockerfile -t "$IMAGE" . >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null

echo "==> helm install"
helm install "$RELEASE" deploy/helm/concord --set image.tag=dev --wait --timeout 150s >/dev/null

echo "==> wait for rollout"
kubectl --context "$CTX" rollout status "deploy/$RELEASE-concord" --timeout=120s
kubectl --context "$CTX" rollout status "statefulset/$RELEASE-concord-dragonfly" --timeout=120s

echo "==> port-forward the concord RPC service"
kubectl --context "$CTX" port-forward "svc/$RELEASE-concord" 8080:8080 >/tmp/concord-chart-pf.log 2>&1 &
PF_PID=$!

RPC=http://localhost:8080/concord.v1.CoordinationService
call() { curl -s -X POST "$RPC/$1" -H 'Content-Type: application/json' -d "$2"; }

# Drive the version check, retrying to absorb port-forward + store readiness
# (the obs-smoke pattern): record a read at v1, then a stale edit (v2) must
# BLOCK and a matching edit (v1) must ALLOW.
echo -n "==> drive version check (stale blocks, matching allows): "
stale="" allow="" ok=""
for _ in $(seq 1 30); do
  if call RecordRead '{"actorId":"e2e","path":"x.go","hash":"v1"}' >/dev/null 2>&1; then
    stale=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v2"}')
    allow=$(call CheckEdit '{"actorId":"e2e","path":"x.go","currentHash":"v1"}')
    if echo "$stale" | grep -q 'changed since you last read it' \
      && echo "$allow" | grep -q '"allowed":true'; then
      ok=1
      break
    fi
  fi
  sleep 1
done

if [ -z "$ok" ]; then
  echo "FAIL"
  echo "  stale response: $stale"
  echo "  allow response: $allow"
  exit 1
fi
echo "ok"

echo ""
echo "chart-test PASSED: in-cluster concord + Dragonfly — stale edit BLOCKED, matching edit ALLOWED"
