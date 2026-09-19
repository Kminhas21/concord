# Minimal image for the concord daemon, used by the local observability compose
# stack. Multi-stage: build a static binary, ship it on distroless. (The full
# containerize+Helm+registry story is a later platform piece; this is just
# enough to run the daemon under docker compose.)
# golang:1.26.5 matches go.mod's `go 1.26.5`, so the build needs no GOTOOLCHAIN
# download and stays hermetic.
FROM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/concord ./cmd/concord

# distroless static-debian12:nonroot, pinned by digest for reproducibility.
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/concord /concord
# Coordination RPC and Prometheus /metrics, respectively.
EXPOSE 8080 9464
ENTRYPOINT ["/concord"]
