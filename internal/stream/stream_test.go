package stream_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/stream"
	"github.com/Kminhas21/concord/test"
)

func TestPublishRoundTrip(t *testing.T) {
	ctx := context.Background()
	url := test.StartNATS(t)

	pub, err := stream.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	want := event.Event{
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    event.TypeEditBlocked,
		ActorID: "agent-7",
		Paths:   []string{"src/auth/login.go"},
		Reason:  event.ReasonStale,
		Detail:  map[string]string{"intent_text": "add auth"},
	}
	if err := pub.Publish(ctx, want); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := consume(t, url, 1)
	if len(got) != 1 {
		t.Fatalf("consumed %d events, want 1", len(got))
	}
	e := got[0]
	if e.Type != want.Type || e.ActorID != want.ActorID || e.Reason != want.Reason ||
		len(e.Paths) != 1 || e.Paths[0] != want.Paths[0] || e.Detail["intent_text"] != "add auth" {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", e, want)
	}
	if !e.TS.Equal(want.TS) {
		t.Fatalf("timestamp round-trip: got %v want %v", e.TS, want.TS)
	}
}

func TestDurableReplayAfterProducerGone(t *testing.T) {
	ctx := context.Background()
	url := test.StartNATS(t)

	pub, err := stream.Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	const n = 5
	for i := 0; i < n; i++ {
		if err := pub.Publish(ctx, event.Event{Type: event.TypeEditAllowed, ActorID: fmt.Sprintf("a%d", i)}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}
	// The producer goes away entirely; the durable stream must still hold the
	// events for a freshly-connecting consumer (the event-web restart story).
	if err := pub.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := consume(t, url, n)
	if len(got) != n {
		t.Fatalf("replayed %d events after producer gone, want %d", len(got), n)
	}
}

// consume opens a fresh NATS connection (a stand-in for event-web), creates an
// ephemeral consumer on the concord stream, and fetches up to n events.
func consume(t *testing.T, url string, n int) []event.Event {
	t.Helper()
	ctx := context.Background()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("consumer connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("consumer jetstream: %v", err)
	}
	s, err := js.Stream(ctx, stream.StreamName)
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	cons, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	msgs, err := cons.Fetch(n, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	var out []event.Event
	for m := range msgs.Messages() {
		var e event.Event
		if err := json.Unmarshal(m.Data(), &e); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, e)
		_ = m.Ack()
	}
	if err := msgs.Error(); err != nil {
		t.Fatalf("fetch error: %v", err)
	}
	return out
}
