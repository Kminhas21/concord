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

	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/rpcaddr"
	"github.com/Kminhas21/concord/internal/store"
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
	return cfg, nil
}

// run serves the CoordinationService on ln until ctx is cancelled, then shuts
// down gracefully. It returns when shutdown completes or serving fails.
func run(ctx context.Context, ln net.Listener, cfg config) error {
	rs := store.NewRedisStore(cfg.dragonflyAddr, cfg.intentTTL)
	svc := coordination.NewService(rs, rs)
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
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
			_ = rs.Close()
			return err
		}
	}

	log.Println("concord: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	if cerr := rs.Close(); cerr != nil {
		log.Printf("concord: closing store: %v", cerr)
	}
	log.Println("concord: stopped")
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, ln, cfg); err != nil {
		log.Fatalf("concord: %v", err)
	}
}
