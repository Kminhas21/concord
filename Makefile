SHELL := bash
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: setup generate build test gate fmt obs-smoke obs-up obs-down

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
