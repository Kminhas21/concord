package test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

// TestStartDragonflyAcceptsPing proves the container harness: a freshly booted
// Dragonfly answers a Redis PING on the returned address.
func TestStartDragonflyAcceptsPing(t *testing.T) {
	addr := StartDragonfly(t)

	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Dragonfly did not answer PING at %s: %v", addr, err)
	}
}
