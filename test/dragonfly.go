// Package test provides shared harness helpers for concord's integration tests.
package test

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const dragonflyImage = "docker.dragonflydb.io/dragonflydb/dragonfly:latest"

// StartDragonfly boots an ephemeral Dragonfly container and returns its
// host:port address. The container is terminated when the test finishes. When
// the Docker daemon is unavailable it fails the test with a clear message
// naming the likely cause, rather than a raw connection error.
func StartDragonfly(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dragonflyImage,
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start Dragonfly container (is the Docker daemon running?): %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve Dragonfly host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatalf("resolve Dragonfly port: %v", err)
	}
	return host + ":" + port.Port()
}
