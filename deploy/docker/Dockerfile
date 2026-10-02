# Canonical concord image. One multi-stage, static, distroless build carrying
# both binaries — /concord (default entrypoint) and /event-web (override). It is
# the single image referenced by the Helm chart (deploy/helm/concord) and the
# observability compose stack, built locally by `make image` and published
# multi-arch to GHCR by the release workflow (buildx) on a version tag.
# (GoReleaser handles the binaries + GitHub Release, not the image — see DECISIONS.)
#
# golang:1.26.5 matches go.mod's `go 1.26.5`, so the build needs no GOTOOLCHAIN
# download and stays hermetic.
#
# --platform=$BUILDPLATFORM pins the build stage to the native build host (never
# emulated), and we cross-compile to the requested TARGETOS/TARGETARCH with Go's
# own cross-compilation. This makes the one canonical Dockerfile correctly
# multi-arch (buildx fills BUILDPLATFORM/TARGETOS/TARGETARCH): a plain
# `docker build` builds for the host arch (native), and a buildx
# `--platform linux/amd64,linux/arm64` build cross-compiles both fast — the final
# distroless stage only COPYs the binary, so no emulation is needed anywhere.
FROM --platform=$BUILDPLATFORM golang:1.26.5 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/concord ./cmd/concord
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/event-web ./cmd/event-web

# distroless static-debian12:nonroot, pinned by digest for reproducibility.
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/concord /concord
COPY --from=build /out/event-web /event-web
# concord: coordination RPC + Prometheus /metrics; event-web: the live SSE feed.
# The default entrypoint is the daemon; the event-web service/chart overrides it.
EXPOSE 8080 9464 8081
ENTRYPOINT ["/concord"]
