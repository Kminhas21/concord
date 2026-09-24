package stream_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/stream"
	"github.com/Kminhas21/concord/test"
)

// TestNATSDownIsDroppedNotFatal proves the real transport half of the
// best-effort invariant (ADR-0009): with NATS taken down, a publish errors and
// the async emitter drops the event (counting it) rather than blocking or
// failing. The other half — that a failing transport never changes a CheckEdit
// verdict — is proven in the normal gate by
// coordination.TestTelemetryFailureNeverBreaksCheckEdit; together they cover the
// invariant against a real killed transport.
func TestNATSDownIsDroppedNotFatal(t *testing.T) {
	ctx := context.Background()
	url, terminate, err := test.StartNATSCtx(ctx)
	if err != nil {
		t.Fatalf("start NATS: %v", err)
	}
	pub, err := stream.Connect(ctx, url)
	if err != nil {
		terminate()
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	// Take the transport down.
	terminate()

	// A direct publish now errors (bounded, so a reconnecting client can't hang).
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pub.Publish(pubCtx, event.Event{Type: event.TypeEditAllowed, ActorID: "x"}); err == nil {
		t.Fatal("Publish succeeded with NATS down; expected an error")
	}

	// Through the async emitter, a down transport increments the drop count and
	// never blocks the emitting goroutine.
	var (
		mu    sync.Mutex
		drops int
	)
	em := event.NewAsyncEmitter(pub, 16, func() { mu.Lock(); drops++; mu.Unlock() })
	em.Emit(event.Event{Type: event.TypeEditBlocked, ActorID: "y"})
	if err := em.Close(); err != nil { // drains: the buffered event fails to publish → drop
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	d := drops
	mu.Unlock()
	if d == 0 {
		t.Fatal("no events dropped with NATS down; best-effort drop accounting failed")
	}
}
