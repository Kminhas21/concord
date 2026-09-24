// Command event-web serves concord's live event feed: it subscribes to the
// JetStream event stream, keeps a bounded ring of recent events, and streams
// them to browsers over Server-Sent Events on a single static page. It is a
// separate process from the daemon (the daemon never consumes).
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Kminhas21/concord/internal/eventweb"
	"github.com/Kminhas21/concord/internal/stream"
)

const (
	defaultAddr   = ":8081"
	defaultBuffer = 200
	// connectTimeout bounds the wait for NATS at startup (it retries within this).
	connectTimeout = 30 * time.Second
)

func main() {
	natsURL := os.Getenv("CONCORD_NATS_URL")
	if natsURL == "" {
		log.Fatal("event-web: CONCORD_NATS_URL is required")
	}
	addr := defaultAddr
	if v := os.Getenv("CONCORD_EVENTWEB_ADDR"); v != "" {
		addr = v
	}
	bufSize := defaultBuffer
	if v := os.Getenv("CONCORD_EVENTWEB_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			bufSize = n
		} else {
			log.Fatalf("event-web: invalid CONCORD_EVENTWEB_BUFFER %q", v)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := eventweb.NewHub(bufSize)

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	cons, err := stream.Consume(connectCtx, natsURL, hub.Add)
	cancel()
	if err != nil {
		log.Fatalf("event-web: subscribe to %s: %v", natsURL, err)
	}
	defer func() { _ = cons.Close() }()

	// BaseContext is cancelled on shutdown so in-flight SSE handlers (which block
	// until their request context is done) unblock immediately, rather than
	// making Shutdown wait out its whole deadline while a browser is connected.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		Addr:        addr,
		Handler:     eventweb.Handler(hub),
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("event-web on %s (nats=%s, buffer=%d)", addr, natsURL, bufSize)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("event-web: serve: %v", err)
		}
	}

	cancelBase() // release connected SSE clients before draining
	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("event-web: stopped")
}
