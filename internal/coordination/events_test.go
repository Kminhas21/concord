package coordination_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/store"
)

// captureEmitter records every event the service emits, synchronously, so a
// test can drive an RPC and assert what came out.
type captureEmitter struct {
	mu  sync.Mutex
	got []event.Event
}

func (c *captureEmitter) Emit(e event.Event) {
	c.mu.Lock()
	c.got = append(c.got, e)
	c.mu.Unlock()
}

func (c *captureEmitter) ofType(t string) []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []event.Event
	for _, e := range c.got {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// only returns the single event of type t, failing if there is not exactly one.
func (c *captureEmitter) only(t *testing.T, typ string) event.Event {
	t.Helper()
	es := c.ofType(typ)
	if len(es) != 1 {
		t.Fatalf("want exactly 1 %s event, got %d (%+v)", typ, len(es), es)
	}
	return es[0]
}

func eventsFixture(t *testing.T) (concordv1connect.CoordinationServiceClient, *captureEmitter) {
	return eventsFixtureTTL(t, 10*time.Minute)
}

// eventsFixtureTTL is eventsFixture with a chosen intent TTL. The store is wired
// with an expiry observer that routes intent_expired into the same capture
// emitter — the realistic production wiring — so the intent_expired event can be
// driven through the RPC seam (a query triggers the lazy eviction).
func eventsFixtureTTL(t *testing.T, intentTTL time.Duration) (concordv1connect.CoordinationServiceClient, *captureEmitter) {
	t.Helper()
	cap := &captureEmitter{}
	rs := store.NewRedisStore(dragonflyAddr, intentTTL, store.WithExpiryObserver(func(actorID string) {
		cap.Emit(event.Event{TS: time.Now().UTC(), Type: event.TypeIntentExpired, ActorID: actorID})
	}))
	t.Cleanup(func() { _ = rs.Close() })
	svc := coordination.NewService(rs, rs, coordination.WithEmitter(cap))
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)
	return client, cap
}

func TestReadRecordedEvent(t *testing.T) {
	client, cap := eventsFixture(t)
	recordRead(t, client, "ev-read", "a.go", "v1")

	e := cap.only(t, event.TypeReadRecorded)
	if e.ActorID != "ev-read" || len(e.Paths) != 1 || e.Paths[0] != "a.go" {
		t.Fatalf("read_recorded = %+v", e)
	}
	if e.TS.IsZero() {
		t.Fatal("event timestamp not stamped")
	}
}

func TestEditAllowedAndBlockedEvents(t *testing.T) {
	client, cap := eventsFixture(t)
	ctx := context.Background()

	// allowed (hash matches)
	recordRead(t, client, "ev-a", "a.go", "v1")
	mustCheckEdit(t, client, "ev-a", "a.go", "v1")
	if e := cap.only(t, event.TypeEditAllowed); e.ActorID != "ev-a" || e.Reason != "" || e.LatencyMS < 0 {
		t.Fatalf("edit_allowed = %+v", e)
	}

	// blocked_stale
	client2, cap2 := eventsFixture(t)
	recordRead(t, client2, "ev-b", "b.go", "v1")
	if _, err := client2.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{ActorId: "ev-b", Path: "b.go", CurrentHash: "v2"})); err != nil {
		t.Fatal(err)
	}
	if e := cap2.only(t, event.TypeEditBlocked); e.Reason != event.ReasonStale {
		t.Fatalf("edit_blocked reason = %q, want %q", e.Reason, event.ReasonStale)
	}

	// blocked_no_read (file exists, never read)
	client3, cap3 := eventsFixture(t)
	mustCheckEdit(t, client3, "ev-c", "c.go", "v9")
	if e := cap3.only(t, event.TypeEditBlocked); e.Reason != event.ReasonNoRead {
		t.Fatalf("edit_blocked reason = %q, want %q", e.Reason, event.ReasonNoRead)
	}

	// allowed_new_file → edit_allowed, no reason
	client4, cap4 := eventsFixture(t)
	mustCheckEdit(t, client4, "ev-d", "d.go", "")
	if e := cap4.only(t, event.TypeEditAllowed); e.Reason != "" {
		t.Fatalf("new-file edit_allowed reason = %q, want empty", e.Reason)
	}
}

func TestIntentAndReconcileEvents(t *testing.T) {
	client, cap := eventsFixture(t)

	registerPredicted(t, client, "ev-reg", "add auth", "src/auth/login.go")
	if e := cap.only(t, event.TypeIntentRegistered); e.ActorID != "ev-reg" ||
		len(e.Paths) != 1 || e.Detail["intent_text"] != "add auth" {
		t.Fatalf("intent_registered = %+v", e)
	}

	appendActual(t, client, "ev-reg", "src/auth/token.go")
	if e := cap.only(t, event.TypeActualAppended); e.Paths[0] != "src/auth/token.go" {
		t.Fatalf("actual_appended = %+v", e)
	}

	if _, err := client.ReconcileFileChange(context.Background(), connect.NewRequest(&concordv1.ReconcileFileChangeRequest{
		ActorId: "ev-reg", Path: "src/auth/login.go", NewHash: "vX",
	})); err != nil {
		t.Fatal(err)
	}
	if e := cap.only(t, event.TypeReconciled); e.Paths[0] != "src/auth/login.go" {
		t.Fatalf("reconciled = %+v", e)
	}
}

func TestOverlapAndDivergenceEvents(t *testing.T) {
	flushDragonfly(t)
	client, cap := eventsFixture(t)

	registerPredicted(t, client, "ev-ov-a", "add auth", "u/login.go")
	appendActual(t, client, "ev-ov-a", "v/other.go") // divergent (different dir)
	registerPredicted(t, client, "ev-ov-b", "tweak auth", "u/login.go")

	if _, err := client.QueryIntent(context.Background(), connect.NewRequest(&concordv1.QueryIntentRequest{Paths: []string{"u/login.go"}})); err != nil {
		t.Fatal(err)
	}

	if got := len(cap.ofType(event.TypeOverlapReported)); got != 2 {
		t.Fatalf("overlap_reported events = %d, want 2 (both actors predicting u/login.go)", got)
	}
	div := cap.ofType(event.TypeDivergenceDetected)
	if len(div) != 1 || div[0].ActorID != "ev-ov-a" || len(div[0].Paths) != 1 || div[0].Paths[0] != "v/other.go" {
		t.Fatalf("divergence_detected = %+v, want one for ev-ov-a naming v/other.go", div)
	}
}

func TestIntentExpiredEvent(t *testing.T) {
	flushDragonfly(t)
	client, cap := eventsFixtureTTL(t, 2*time.Second)

	registerPredicted(t, client, "ev-exp", "short-lived", "z/tmp.go")

	// After the TTL lapses the record's keys expire; the next query's ListIntents
	// evicts the lingering id and fires the expiry observer → intent_expired.
	deadline := time.Now().Add(9 * time.Second)
	for {
		if _, err := client.QueryIntent(context.Background(), connect.NewRequest(&concordv1.QueryIntentRequest{Paths: []string{"z/tmp.go"}})); err != nil {
			t.Fatal(err)
		}
		if es := cap.ofType(event.TypeIntentExpired); len(es) > 0 {
			if es[0].ActorID != "ev-exp" {
				t.Fatalf("intent_expired actor = %q, want ev-exp", es[0].ActorID)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no intent_expired event after TTL lapse")
		}
		time.Sleep(300 * time.Millisecond)
	}
}
