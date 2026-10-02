SHELL := bash
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: setup generate build test gate fmt obs-smoke obs-up obs-down image chart-lint

# CHART is the concord coordination-unit Helm chart.
CHART ?= deploy/helm/concord

# IMAGE is the canonical concord image tag. The local/dev tag is used by the
# observability stack and the kind chart test (loaded via `kind load`, never
# pulled); GoReleaser publishes the real multi-arch image to GHCR on a tag.
IMAGE ?= ghcr.io/kminhas21/concord:dev

# One-time install of the protobuf/Connect codegen tools.
setup:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

generate:
	buf lint
	buf generate

build:
	go build ./...

test:
	go test ./...

# The green gate: run before every commit. Requires the Docker daemon (tests
# boot a real ephemeral Dragonfly).
gate: generate
	@test -z "$$(gofmt -l cmd internal test)" || { echo "gofmt needed in:"; gofmt -l cmd internal test; exit 1; }
	go vet ./...
	go build ./...
	go test ./...

# image: build the canonical concord image (both binaries) as the local dev tag.
image:
	docker build -f deploy/docker/Dockerfile -t $(IMAGE) .

# chart-lint: fast chart checks with no cluster — structural lint, kubeconform
# schema-validation of every rendered manifest against the real Kubernetes API,
# and helm-unittest template-logic tests. The kind end-to-end test
# (make chart-test) comes in ticket 05.
#
# helm-unittest is a one-time plugin install:
#   helm plugin install https://github.com/helm-unittest/helm-unittest --verify=false
# (--verify=false is needed on Helm 4; drop it on Helm 3.)
chart-lint:
	helm lint $(CHART)
	set -o pipefail; helm template $(CHART) | kubeconform -strict -summary
	helm unittest $(CHART)

# obs-smoke: boot the observability compose stack and assert metrics flow
# end-to-end (daemon -> Prometheus -> Grafana), then tear it down. The infra
# verification gate for the observability piece; needs the Docker daemon. Not on
# the CI matrix (compose is too heavy for per-push).
obs-smoke:
	bash scripts/obs-smoke.sh

# obs-up / obs-down: bring the stack up (Grafana on http://localhost:3000,
# Prometheus on :9090) for hands-on exploration, and tear it back down.
obs-up:
	docker compose -f deploy/observability/docker-compose.yml up -d --build
obs-down:
	docker compose -f deploy/observability/docker-compose.yml down -v
