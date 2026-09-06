SHELL := bash
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: setup generate build test gate fmt

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
