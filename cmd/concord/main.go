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
	"github.com/Kminhas21/concord/internal/rpcaddr"
	"github.com/Kminhas21/concord/internal/store"
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
	return cfg, nil
}

// run serves the CoordinationService on ln until ctx is cancelled, then shuts
// down gracefully. When metricsLn is non-nil the daemon is instrumented and
// serves Prometheus /metrics on it; when nil, observability is off and
// coordination is byte-for-byte what it was before (ADR-0009). It returns when
// shutdown completes or serving fails.
func run(ctx context.Context, ln, metricsLn net.Listener, cfg config) error {
	rs := store.NewRedisStore(cfg.dragonflyAddr, cfg.intentTTL)

	var (
		svcOpts     []coordination.Option
		handlerOpts []connect.HandlerOption
		provider    *telemetry.Provider
		metricsSrv  *http.Server
	)
	if metricsLn != nil {
		prov, err := telemetry.NewProvider()
		if err != nil {
			_ = rs.Close()
			return err
		}
		provider = prov
		metrics := telemetry.NewMetrics(prov.Meter(), func(ctx context.Context) (int64, error) {
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
			shutdownTelemetry(metricsSrv, provider)
			_ = rs.Close()
			return err
		}
	}

	log.Println("concord: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
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
