package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// natsImage is the NATS server image, JetStream enabled via the "-js" flag.
const natsImage = "nats:2.10-alpine"

// StartNATSCtx boots an ephemeral JetStream-enabled NATS server and returns its
// client URL plus a terminate func the caller must invoke. It mirrors
// StartDragonflyCtx for the event-plane integration tests.
func StartNATSCtx(ctx context.Context) (url string, terminate func(), err error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        natsImage,
			Cmd:          []string{"-js"},
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready"),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, err
	}
	terminate = func() { _ = container.Terminate(ctx) }

	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		return "", nil, fmt.Errorf("resolve NATS host: %w", err)
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		terminate()
		return "", nil, fmt.Errorf("resolve NATS port: %w", err)
	}
	return fmt.Sprintf("nats://%s:%s", host, port.Port()), terminate, nil
}

// StartNATS boots an ephemeral JetStream NATS server for a test and returns its
// client URL. The container is terminated when the test finishes.
func StartNATS(t *testing.T) string {
	t.Helper()
	url, terminate, err := StartNATSCtx(context.Background())
	if err != nil {
		t.Fatalf("start NATS container (is the Docker daemon running?): %v", err)
	}
	t.Cleanup(terminate)
	return url
}
