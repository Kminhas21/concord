# Minimal image for the concord daemon, used by the local observability compose
# stack. Multi-stage: build a static binary, ship it on distroless. (The full
# containerize+Helm+registry story is a later platform piece; this is just
# enough to run the daemon under docker compose.)
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/concord ./cmd/concord

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/concord /concord
# Coordination RPC and Prometheus /metrics, respectively.
EXPOSE 8080 9464
ENTRYPOINT ["/concord"]
