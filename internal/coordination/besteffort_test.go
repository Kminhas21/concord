package coordination_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/store"
	"github.com/Kminhas21/concord/internal/telemetry"
)

// erroringSink fails every publish, simulating a down/unhealthy event transport.
type erroringSink struct{}

func (erroringSink) Publish(context.Context, event.Event) error {
	return errors.New("transport down")
}

// TestTelemetryFailureNeverBreaksCheckEdit is the load-bearing best-effort
// invariant (ADR-0009), in the normal gate: with the event transport failing,
// CheckEdit still returns the correct verdict and concord_events_dropped_total
// advances. Telemetry is never a correctness or availability dependency.
func TestTelemetryFailureNeverBreaksCheckEdit(t *testing.T) {
	prov, err := telemetry.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = prov.Shutdown(context.Background()) })

	rs := store.NewRedisStore(dragonflyAddr, 10*time.Minute)
	t.Cleanup(func() { _ = rs.Close() })
	metrics := telemetry.NewMetrics(prov.Meter(), nil)

	emitter := event.NewAsyncEmitter(erroringSink{}, 16, func() { metrics.EventDropped(context.Background()) })
	t.Cleanup(func() { _ = emitter.Close() })

	svc := coordination.NewService(rs, rs, coordination.WithMetrics(metrics), coordination.WithEmitter(emitter))
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(path, handler)
	mux.Handle("/metrics", prov.Handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)

	ctx := context.Background()
	const actor, p = "be-actor", "be/login.go"

	// The verdict must be correct despite the failing transport: a stale edit blocks.
	recordRead(t, client, actor, p, "v1")
	resp, err := client.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{ActorId: actor, Path: p, CurrentHash: "v2"}))
	if err != nil {
		t.Fatalf("CheckEdit errored under telemetry failure: %v", err)
	}
	if resp.Msg.GetAllowed() {
		t.Fatal("stale CheckEdit was allowed — telemetry failure changed the verdict")
	}

	// The dropped events (read_recorded + edit_blocked) are drained asynchronously
	// and each fails to publish, so the drop counter advances. Poll to absorb the
	// async drain.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, ok := metricValue(t, scrapeMetrics(t, srv.Client(), srv.URL), "concord_events_dropped_total"); ok && v > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("concord_events_dropped_total did not advance under a failing transport")
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// scrapeMetrics fetches the /metrics text from srv.
func scrapeMetrics(t *testing.T, c *http.Client, base string) string {
	t.Helper()
	resp, err := c.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b := make([]byte, 0, 64*1024)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(b)
}
