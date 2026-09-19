// Command concord runs the coordination daemon: a single long-lived process
// that serves the CoordinationService over Connect on a local address. Each
// hook client makes one RPC to it.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/rpcaddr"
	"github.com/Kminhas21/concord/internal/store"
	"github.com/Kminhas21/concord/internal/stream"
	"github.com/Kminhas21/concord/internal/telemetry"
)

// defaultDragonflyAddr is where the daemon expects Dragonfly when
// CONCORD_DRAGONFLY_ADDR is unset.
const defaultDragonflyAddr = "127.0.0.1:6379"

// defaultIntentTTL is the silence window after which an untouched intent record
// expires. 600s covers p99.9 of active-work inter-tool gaps (docs/budgets.md).
const defaultIntentTTL = 600 * time.Second

type config struct {
	addr          string
	dragonflyAddr string
	intentTTL     time.Duration
	// metricsAddr is where the Prometheus /metrics endpoint listens. Empty
	// disables observability entirely: the daemon coordinates as before, exposing
	// no metrics (telemetry is strictly optional — ADR-0009).
	metricsAddr string
	// natsURL is the NATS JetStream URL for domain-event emission. Empty disables
	// event emission; a connect failure at startup also degrades to disabled,
	// never stopping the daemon (events are strictly best-effort — ADR-0009).
	natsURL string
}

func configFromEnv() (config, error) {
	cfg := config{addr: rpcaddr.Default, dragonflyAddr: defaultDragonflyAddr, intentTTL: defaultIntentTTL}
	if v := os.Getenv("CONCORD_ADDR"); v != "" {
		cfg.addr = v
	}
	if v := os.Getenv("CONCORD_DRAGONFLY_ADDR"); v != "" {
		cfg.dragonflyAddr = v
	}
	if v := os.Getenv("CONCORD_INTENT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return cfg, err
		}
		cfg.intentTTL = d
	}
	cfg.metricsAddr = os.Getenv("CONCORD_METRICS_ADDR")
	cfg.natsURL = os.Getenv("CONCORD_NATS_URL")
	return cfg, nil
}

// eventBufferSize is the depth of the async emitter's buffer: events beyond it
// are dropped (and counted) rather than ever blocking a coordination handler.
const eventBufferSize = 1024

// run serves the CoordinationService on ln until ctx is cancelled, then shuts
// down gracefully. When metricsLn is non-nil the daemon is instrumented and
// serves Prometheus /metrics on it; when nil, observability is off and
// coordination is byte-for-byte what it was before (ADR-0009). It returns when
// shutdown completes or serving fails.
func run(ctx context.Context, ln, metricsLn net.Listener, cfg config) error {
	// Forward-declared so the store's expiry observer can reference the metrics
	// and emitter built after it. The observer only fires at runtime (during a
	// ListIntents scan), by which point wiring is complete.
	var (
		metrics *telemetry.Metrics
		emitter *event.AsyncEmitter
	)
	onExpire := func(actorID string) {
		if emitter != nil {
			emitter.Emit(event.Event{TS: time.Now().UTC(), Type: event.TypeIntentExpired, ActorID: actorID})
		}
		if metrics != nil {
			metrics.IntentExpired(context.Background())
		}
	}
	rs := store.NewRedisStore(cfg.dragonflyAddr, cfg.intentTTL, store.WithExpiryObserver(onExpire))

	var (
		svcOpts     []coordination.Option
		handlerOpts []connect.HandlerOption
		provider    *telemetry.Provider
		metricsSrv  *http.Server
		publisher   *stream.Publisher
	)
	if metricsLn != nil {
		prov, err := telemetry.NewProvider()
		if err != nil {
			_ = rs.Close()
			return err
		}
		provider = prov
		metrics = telemetry.NewMetrics(prov.Meter(), func(ctx context.Context) (int64, error) {
			recs, err := rs.ListIntents(ctx)
			return int64(len(recs)), err
		})
		svcOpts = append(svcOpts, coordination.WithMetrics(metrics))
		handlerOpts = append(handlerOpts, connect.WithInterceptors(telemetry.Interceptor(metrics)))

		mmux := http.NewServeMux()
		mmux.Handle("/metrics", prov.Handler)
		metricsSrv = &http.Server{Handler: mmux}
		go func() {
			log.Printf("concord metrics on %s/metrics", metricsLn.Addr())
			if err := metricsSrv.Serve(metricsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("concord: metrics server: %v", err)
			}
		}()
	}

	if cfg.natsURL != "" {
		// Events are strictly best-effort: a NATS failure at startup degrades to
		// emission-disabled and the daemon coordinates normally (ADR-0009).
		pub, err := stream.Connect(ctx, cfg.natsURL)
		if err != nil {
			log.Printf("concord: event emission disabled (nats connect: %v)", err)
		} else {
			publisher = pub
			onDrop := func() {
				if metrics != nil {
					metrics.EventDropped(context.Background())
				}
			}
			emitter = event.NewAsyncEmitter(pub, eventBufferSize, onDrop)
			svcOpts = append(svcOpts, coordination.WithEmitter(emitter))
			log.Printf("concord events -> nats %s (subject %s)", cfg.natsURL, stream.Subject)
		}
	}

	// closeEvents flushes buffered events to NATS, then drains the connection.
	// Order matters: the emitter drains through the publisher, so it closes first.
	closeEvents := func() {
		if emitter != nil {
			_ = emitter.Close()
		}
		if publisher != nil {
			_ = publisher.Close()
		}
	}

	svc := coordination.NewService(rs, rs, svcOpts...)
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc, handlerOpts...)
	mux.Handle(path, handler)

	srv := &http.Server{Handler: mux}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("concord listening on %s (dragonfly=%s, intent_ttl=%s)", ln.Addr(), cfg.dragonflyAddr, cfg.intentTTL)
		serveErr <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeEvents()
			shutdownTelemetry(metricsSrv, provider)
			_ = rs.Close()
			return err
		}
	}

	log.Println("concord: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	closeEvents() // flush buffered events before tearing down telemetry/store
	shutdownTelemetry(metricsSrv, provider)
	if cerr := rs.Close(); cerr != nil {
		log.Printf("concord: closing store: %v", cerr)
	}
	log.Println("concord: stopped")
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// shutdownTelemetry stops the metrics server and meter provider if they were
// started. It is best-effort: telemetry teardown never fails a clean shutdown.
func shutdownTelemetry(metricsSrv *http.Server, provider *telemetry.Provider) {
	if metricsSrv == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(shutdownCtx)
	if provider != nil {
		_ = provider.Shutdown(shutdownCtx)
	}
}

func main() {
	cfg, err := configFromEnv()
	if err != nil {
		log.Fatalf("concord: invalid CONCORD_INTENT_TTL: %v", err)
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		log.Fatalf("concord: listen on %s: %v", cfg.addr, err)
	}

	var metricsLn net.Listener
	if cfg.metricsAddr != "" {
		metricsLn, err = net.Listen("tcp", cfg.metricsAddr)
		if err != nil {
			log.Fatalf("concord: listen on metrics addr %s: %v", cfg.metricsAddr, err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, ln, metricsLn, cfg); err != nil {
		log.Fatalf("concord: %v", err)
	}
}
