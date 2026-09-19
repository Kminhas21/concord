package coordination_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/store"
	"github.com/Kminhas21/concord/internal/telemetry"
	"github.com/redis/go-redis/v9"
)

// metricsFixture wires a Service whose decisions feed a real OTel meter exported
// on a Prometheus /metrics endpoint. It returns a Connect client (the seam under
// test) and a scrape func that fetches the current /metrics text. Metrics are an
// external, scrapeable surface — tests drive RPCs through the client and assert
// what the endpoint exposes, never internal counters.
func metricsFixture(t *testing.T) (concordv1connect.CoordinationServiceClient, func() string) {
	return metricsFixtureTTL(t, 10*time.Minute)
}

// metricsFixtureTTL is metricsFixture with a chosen intent TTL, for the gauge
// test that watches records lapse on silence.
func metricsFixtureTTL(t *testing.T, intentTTL time.Duration) (concordv1connect.CoordinationServiceClient, func() string) {
	t.Helper()

	prov, err := telemetry.NewProvider()
	if err != nil {
		t.Fatalf("telemetry.NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = prov.Shutdown(context.Background()) })

	rs := store.NewRedisStore(dragonflyAddr, intentTTL)
	t.Cleanup(func() { _ = rs.Close() })
	metrics := telemetry.NewMetrics(prov.Meter(), func(ctx context.Context) (int64, error) {
		recs, err := rs.ListIntents(ctx)
		return int64(len(recs)), err
	})
	svc := coordination.NewService(rs, rs, coordination.WithMetrics(metrics))

	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc, connect.WithInterceptors(telemetry.Interceptor(metrics)))
	mux.Handle(path, handler)
	mux.Handle("/metrics", prov.Handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)
	scrape := func() string {
		resp, err := srv.Client().Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatalf("scrape /metrics: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read /metrics body: %v", err)
		}
		return string(body)
	}
	return client, scrape
}

// metricValue returns the value of the first Prometheus sample whose line begins
// with name and contains every given label fragment (e.g. `decision="blocked_stale"`).
// It returns (0, false) when no such sample is present.
func metricValue(t *testing.T, body, name string, labels ...string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		// Skip other metrics that share this name as a prefix (e.g. _bucket/_sum).
		rest := line[len(name):]
		if rest != "" && rest[0] != '{' && rest[0] != ' ' {
			continue
		}
		ok := true
		for _, l := range labels {
			if !strings.Contains(line, l) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parse metric value from %q: %v", line, err)
		}
		return v, true
	}
	return 0, false
}

func TestCheckEditBlockedStaleIncrementsCounter(t *testing.T) {
	client, scrape := metricsFixture(t)
	ctx := context.Background()
	const actor, path = "metrics-stale", "src/auth/login.go"

	// Actor read the file at hash "v1"; the file is now "v2" on disk → stale block.
	recordRead(t, client, actor, path, "v1")
	resp, err := client.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId: actor, Path: path, CurrentHash: "v2",
	}))
	if err != nil {
		t.Fatalf("CheckEdit: %v", err)
	}
	if resp.Msg.GetAllowed() {
		t.Fatal("stale CheckEdit should be blocked")
	}

	v, ok := metricValue(t, scrape(), "concord_checkedit_total", `decision="blocked_stale"`)
	if !ok {
		t.Fatal(`concord_checkedit_total{decision="blocked_stale"} not present`)
	}
	if v != 1 {
		t.Fatalf(`concord_checkedit_total{decision="blocked_stale"} = %v, want 1`, v)
	}
}

// flushDragonfly clears the shared keyspace so a test that asserts an exact
// count over ListIntents is not polluted by other tests' intent records.
//
// WARNING: this wipes the one Dragonfly shared by the whole package (booted in
// TestMain). It is safe ONLY because tests in this package run sequentially — no
// test calls t.Parallel(). Do NOT add t.Parallel() to any test in this package
// while this helper exists, or a flush can nondeterministically erase a
// concurrent test's data. The durable fix, if parallelism is ever wanted, is to
// isolate these tests on their own Redis DB index instead of flushing.
func flushDragonfly(t *testing.T) {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: dragonflyAddr})
	defer func() { _ = c.Close() }()
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
}

func TestCheckEditDecisionLabels(t *testing.T) {
	client, scrape := metricsFixture(t)

	// allowed: read then edit at the same hash.
	recordRead(t, client, "m-allowed", "a.go", "v1")
	mustCheckEdit(t, client, "m-allowed", "a.go", "v1")

	// blocked_no_read: file exists on disk but the actor never read it.
	mustCheckEdit(t, client, "m-noread", "b.go", "v9")

	// allowed_new_file: no read, and no file on disk (empty current hash).
	mustCheckEdit(t, client, "m-newfile", "c.go", "")

	body := scrape()
	for _, want := range []string{"allowed", "blocked_no_read", "allowed_new_file"} {
		v, ok := metricValue(t, body, "concord_checkedit_total", `decision="`+want+`"`)
		if !ok || v != 1 {
			t.Fatalf(`concord_checkedit_total{decision=%q} = %v (present=%v), want 1`, want, v, ok)
		}
	}
}

func TestRPCDurationHistogramObserved(t *testing.T) {
	client, scrape := metricsFixture(t)
	mustCheckEdit(t, client, "m-timing", "d.go", "")

	v, ok := metricValue(t, scrape(), "concord_rpc_duration_seconds_count", `method="CheckEdit"`)
	if !ok {
		t.Fatal(`concord_rpc_duration_seconds_count{method="CheckEdit"} not present`)
	}
	if v < 1 {
		t.Fatalf(`concord_rpc_duration_seconds_count{method="CheckEdit"} = %v, want >= 1`, v)
	}
}

func TestOverlapAndDivergenceCounters(t *testing.T) {
	flushDragonfly(t)
	client, scrape := metricsFixture(t)
	ctx := context.Background()

	// Actor A predicts u/login.go but actually touches a different directory
	// (v/other.go) → divergent, while still overlapping a query for u/login.go.
	// Actor B predicts u/login.go → overlaps, no divergence.
	registerPredicted(t, client, "ov-a", "add auth", "u/login.go")
	appendActual(t, client, "ov-a", "v/other.go")
	registerPredicted(t, client, "ov-b", "tweak auth", "u/login.go")

	_, err := client.QueryIntent(ctx, connect.NewRequest(&concordv1.QueryIntentRequest{Paths: []string{"u/login.go"}}))
	if err != nil {
		t.Fatalf("QueryIntent: %v", err)
	}

	body := scrape()
	if v, ok := metricValue(t, body, "concord_overlaps_total"); !ok || v != 2 {
		t.Fatalf("concord_overlaps_total = %v (present=%v), want 2", v, ok)
	}
	if v, ok := metricValue(t, body, "concord_divergences_total"); !ok || v != 1 {
		t.Fatalf("concord_divergences_total = %v (present=%v), want 1", v, ok)
	}
}

func TestLiveIntentsGaugeReflectsActiveRecords(t *testing.T) {
	flushDragonfly(t)
	// A 5s TTL leaves generous headroom for the "rises to 2" scrape even on a
	// loaded runner (records must not lapse before the first poll observes them),
	// while the fall-poll below allows 9s for expiry to take effect.
	client, scrape := metricsFixtureTTL(t, 5*time.Second)

	registerPredicted(t, client, "g-a", "work a", "g/a.go")
	registerPredicted(t, client, "g-b", "work b", "g/b.go")

	if v, ok := gaugePoll(t, scrape, 2, 4*time.Second); !ok {
		t.Fatalf("concord_live_intents did not reach 2 (last=%v)", v)
	}

	// With no touch, both records lapse on silence and the gauge falls to 0 —
	// no reaper, just Redis TTL + read-path self-heal (ADR-0002). Scraping only
	// reads, so it never refreshes the TTL and keeps a record alive.
	if v, ok := gaugePoll(t, scrape, 0, 9*time.Second); !ok {
		t.Fatalf("concord_live_intents did not fall to 0 after TTL (last=%v)", v)
	}
}

func TestBackpressureCountersStartAtZero(t *testing.T) {
	_, scrape := metricsFixture(t)
	body := scrape()
	for _, name := range []string{"concord_events_dropped_total", "concord_intent_expired_total"} {
		v, ok := metricValue(t, body, name)
		if !ok {
			t.Fatalf("%s not present; the metric surface must be stable before the event plane exists", name)
		}
		if v != 0 {
			t.Fatalf("%s = %v, want 0", name, v)
		}
	}
}

// gaugePoll scrapes until concord_live_intents equals want or timeout elapses.
func gaugePoll(t *testing.T, scrape func() string, want float64, timeout time.Duration) (float64, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last float64
	for time.Now().Before(deadline) {
		if v, ok := metricValue(t, scrape(), "concord_live_intents"); ok {
			last = v
			if v == want {
				return v, true
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return last, false
}

func mustCheckEdit(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, path, currentHash string) {
	t.Helper()
	if _, err := c.CheckEdit(context.Background(), connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId: actor, Path: path, CurrentHash: currentHash,
	})); err != nil {
		t.Fatalf("CheckEdit(%s): %v", actor, err)
	}
}
