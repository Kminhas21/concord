package eventweb_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/eventweb"
	"github.com/Kminhas21/concord/internal/stream"
	"github.com/Kminhas21/concord/test"
)

// TestEndToEndPublishReachesSSE wires the real event plane: a Publisher writes to
// JetStream, a stream.Consumer feeds the Hub, and an SSE browser client receives
// the event — daemon -> NATS -> event-web -> browser.
func TestEndToEndPublishReachesSSE(t *testing.T) {
	ctx := context.Background()
	url := test.StartNATS(t)

	pub, err := stream.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	hub := eventweb.NewHub(100)
	cons, err := stream.Consume(ctx, url, hub.Add)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	t.Cleanup(func() { _ = cons.Close() })

	srv := httptest.NewServer(eventweb.Handler(hub))
	t.Cleanup(srv.Close)

	events, stop := sseClient(t, srv.URL)
	defer stop()

	if err := pub.Publish(ctx, event.Event{
		Type: event.TypeEditBlocked, ActorID: "e2e", Paths: []string{"src/auth/login.go"}, Reason: event.ReasonStale,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// The event travels publisher -> stream -> consumer -> hub -> SSE. Read until
	// it arrives (other events may precede it; allow for the async hop).
	deadline := time.After(15 * time.Second)
	for {
		select {
		case e := <-events:
			if e.ActorID == "e2e" && e.Type == event.TypeEditBlocked && e.Reason == event.ReasonStale {
				return
			}
		case <-deadline:
			t.Fatal("published event never reached the SSE stream end-to-end")
		}
	}
}
