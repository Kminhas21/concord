// Package telemetry holds concord's OpenTelemetry metrics: a Provider that
// exposes a Prometheus /metrics endpoint, the concord-specific instruments, and
// a Connect interceptor that times every RPC. It is the single instrumentation
// foundation for the daemon (the observability architecture and its
// strictly-best-effort trade are recorded in ADR-0009, with the event plane).
// Emission is observation only: it never influences a coordination decision (ADR-0001).
package telemetry

import (
	"context"
	"net/http"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Provider bundles an OTel MeterProvider with the HTTP handler that exposes its
// metrics in Prometheus text format. Meter() sources concord's instruments;
// Handler serves them on /metrics.
type Provider struct {
	mp      *sdkmetric.MeterProvider
	Handler http.Handler
}

// NewProvider builds a MeterProvider backed by a Prometheus exporter and returns
// it alongside the /metrics handler that scrapes it.
func NewProvider() (*Provider, error) {
	reg := promclient.NewRegistry()
	exp, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp))
	return &Provider{
		mp:      mp,
		Handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}, nil
}

// Meter returns the concord-scoped meter used to build instruments.
func (p *Provider) Meter() metric.Meter { return p.mp.Meter("concord") }

// Shutdown flushes and releases the meter provider.
func (p *Provider) Shutdown(ctx context.Context) error { return p.mp.Shutdown(ctx) }

// Metrics holds concord's instruments. Its methods are called at the point each
// coordination decision is made; the Prometheus exporter maps instrument names
// to series (a counter "concord_checkedit" is exposed as "concord_checkedit_total",
// the "concord_rpc_duration" histogram with unit "s" as "concord_rpc_duration_seconds").
type Metrics struct {
	checkEdit     metric.Int64Counter
	overlaps      metric.Int64Counter
	divergences   metric.Int64Counter
	intentExpired metric.Int64Counter
	eventsDropped metric.Int64Counter
	rpcDuration   metric.Float64Histogram
}

// intentCounter reports the number of active intent records, read at scrape time
// to drive the live-intents gauge. It is best-effort: an error is swallowed and
// the gauge simply reports nothing for that collection.
type intentCounter func(context.Context) (int64, error)

// NewMetrics registers concord's instruments on meter. liveIntents is polled at
// scrape time via an observable gauge, so the count falls when a record's TTL
// lapses without any reaper (ADR-0002).
func NewMetrics(meter metric.Meter, liveIntents intentCounter) *Metrics {
	m := &Metrics{}
	m.checkEdit, _ = meter.Int64Counter("concord_checkedit",
		metric.WithDescription("CheckEdit decisions by outcome."))
	m.overlaps, _ = meter.Int64Counter("concord_overlaps",
		metric.WithDescription("Path overlaps reported by intent queries."))
	m.divergences, _ = meter.Int64Counter("concord_divergences",
		metric.WithDescription("Actual footprints diverging from the predicted footprint."))
	m.intentExpired, _ = meter.Int64Counter("concord_intent_expired",
		metric.WithDescription("Intent records that expired on silence."))
	m.eventsDropped, _ = meter.Int64Counter("concord_events_dropped",
		metric.WithDescription("Domain events dropped by best-effort telemetry backpressure."))
	m.rpcDuration, _ = meter.Float64Histogram("concord_rpc_duration",
		metric.WithUnit("s"),
		metric.WithDescription("RPC handler latency by method."))

	// Emit a zero sample for the label-free counters so the metric surface is
	// stable from the first scrape: a fresh daemon's Grafana panels read 0 rather
	// than "no data", and the backpressure counters exist before the event plane
	// wires their real increments (a later ticket).
	ctx := context.Background()
	m.overlaps.Add(ctx, 0)
	m.divergences.Add(ctx, 0)
	m.eventsDropped.Add(ctx, 0)
	m.intentExpired.Add(ctx, 0)

	gauge, _ := meter.Int64ObservableGauge("concord_live_intents",
		metric.WithDescription("Active intent records."))
	if liveIntents != nil {
		_, _ = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
			// Bound the store round-trip so a hung Dragonfly stalls neither the
			// scrape nor the collector goroutine.
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			n, err := liveIntents(cctx)
			if err != nil {
				return nil // best-effort: skip this collection, never fail a scrape
			}
			o.ObserveInt64(gauge, n)
			return nil
		}, gauge)
	}
	return m
}

// CheckEditDecision records one CheckEdit outcome. decision is one of
// "allowed", "blocked_stale", "blocked_no_read", "allowed_new_file".
func (m *Metrics) CheckEditDecision(ctx context.Context, decision string) {
	m.checkEdit.Add(ctx, 1, metric.WithAttributes(attrString("decision", decision)))
}

// OverlapsReported records n path overlaps surfaced by one intent query.
func (m *Metrics) OverlapsReported(ctx context.Context, n int) {
	if n > 0 {
		m.overlaps.Add(ctx, int64(n))
	}
}

// DivergencesDetected records n footprint divergences surfaced by one intent query.
func (m *Metrics) DivergencesDetected(ctx context.Context, n int) {
	if n > 0 {
		m.divergences.Add(ctx, int64(n))
	}
}

// EventDropped records one domain event dropped by best-effort telemetry. Wired
// by the event plane in a later ticket.
func (m *Metrics) EventDropped(ctx context.Context) { m.eventsDropped.Add(ctx, 1) }

// IntentExpired records one intent record expiring on silence. Wired by the
// event plane in a later ticket.
func (m *Metrics) IntentExpired(ctx context.Context) { m.intentExpired.Add(ctx, 1) }

// recordRPC records one RPC's handler latency under its method label.
func (m *Metrics) recordRPC(ctx context.Context, method string, d time.Duration) {
	m.rpcDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attrString("method", method)))
}
