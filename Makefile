SHELL := bash
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: setup generate envtest-assetsbuild test gate fmt obs-smoke obs-up obs-down image chart-lint chart-test

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

# Pinned tool versions. ENVTEST_K8S_VERSION matches kind v0.33's default node
# image (Kubernetes 1.37), which `make chart-test` boots.
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0
SETUP_ENVTEST := go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.2
ENVTEST_K8S_VERSION := 1.37.0

generate:
	buf lint
	buf generate
	$(CONTROLLER_GEN) object paths=./api/v1alpha1 crd paths=./api/v1alpha1 output:crd:dir=deploy/helm/concord-operator/crds

# envtest-assets prints the kube-apiserver/etcd dir; use as
#   export KUBEBUILDER_ASSETS=$$(make -s envtest-assets)
envtest-assets:
	@$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path

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

# chart-test: the end-to-end acceptance gate — boot a kind cluster, load the
# local image, install the chart, and drive a real stale CheckEdit that BLOCKS
# (and a matching one that ALLOWS) against the in-cluster concord+Dragonfly, then
# tear down. Needs docker + kind + helm + kubectl. The kind analog of obs-smoke;
# local/opt-in, not on the CI matrix.
chart-test:
	bash scripts/chart-test.sh

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
