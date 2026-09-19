#!/usr/bin/env bash
# obs-smoke: the verification gate for the observability compose stack (ticket
# 02). Boots concord + Dragonfly + Prometheus + Grafana, then runs the Go smoke
# driver which drives traffic and asserts metrics flow daemon -> Prometheus ->
# Grafana. Always tears the stack (and volumes) down on exit.
#
# Not part of the CI matrix — compose is too heavy for per-push. Run locally:
#   make obs-smoke
set -euo pipefail

cd "$(dirname "$0")/.."
COMPOSE="docker compose -f deploy/observability/docker-compose.yml"

cleanup() { $COMPOSE down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "==> building and starting the observability stack"
$COMPOSE up -d --build

echo "==> running smoke assertions"
go run ./deploy/observability/smoke
