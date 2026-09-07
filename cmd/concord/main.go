// Command concord runs the coordination daemon: a single long-lived process
// that serves the CoordinationService over Connect on a local address. Each
// hook client makes one RPC to it.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/store"
)

// defaultAddr is the loopback address the daemon listens on when CONCORD_ADDR
// is unset. Loopback only: concord is one daemon per machine, never networked.
const defaultAddr = "127.0.0.1:8973"

// defaultDragonflyAddr is where the daemon expects Dragonfly when
// CONCORD_DRAGONFLY_ADDR is unset.
const defaultDragonflyAddr = "127.0.0.1:6379"

// defaultIntentTTL is the silence window after which an untouched intent record
// expires. 600s covers p99.9 of active-work inter-tool gaps (docs/budgets.md).
const defaultIntentTTL = 600 * time.Second

func main() {
	addr := os.Getenv("CONCORD_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	dragonflyAddr := os.Getenv("CONCORD_DRAGONFLY_ADDR")
	if dragonflyAddr == "" {
		dragonflyAddr = defaultDragonflyAddr
	}
	intentTTL := defaultIntentTTL
	if v := os.Getenv("CONCORD_INTENT_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("concord: invalid CONCORD_INTENT_TTL %q: %v", v, err)
		}
		intentTTL = d
	}

	rs := store.NewRedisStore(dragonflyAddr, intentTTL)
	svc := coordination.NewService(rs, rs)
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(path, handler)

	log.Printf("concord listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("concord: %v", err)
	}
}
