# Canonical concord image. One multi-stage, static, distroless build carrying
# both binaries — /concord (default entrypoint) and /event-web (override). It is
# the single image referenced by the Helm chart (deploy/helm/concord) and the
# observability compose stack, built locally by `make image` and published
# multi-arch to GHCR by GoReleaser on a version tag.
#
# golang:1.26.5 matches go.mod's `go 1.26.5`, so the build needs no GOTOOLCHAIN
# download and stays hermetic.
FROM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/concord ./cmd/concord
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/event-web ./cmd/event-web

# distroless static-debian12:nonroot, pinned by digest for reproducibility.
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/concord /concord
COPY --from=build /out/event-web /event-web
# concord: coordination RPC + Prometheus /metrics; event-web: the live SSE feed.
# The default entrypoint is the daemon; the event-web service/chart overrides it.
EXPOSE 8080 9464 8081
ENTRYPOINT ["/concord"]
