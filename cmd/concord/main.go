// Command concord runs the coordination daemon: a single long-lived process
// that serves the CoordinationService over Connect on a local address. Each
// hook client makes one RPC to it.
package main

import (
	"log"
	"net/http"
	"os"

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

func main() {
	addr := os.Getenv("CONCORD_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	dragonflyAddr := os.Getenv("CONCORD_DRAGONFLY_ADDR")
	if dragonflyAddr == "" {
		dragonflyAddr = defaultDragonflyAddr
	}

	svc := coordination.NewService(store.NewRedisStore(dragonflyAddr))
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(path, handler)

	log.Printf("concord listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("concord: %v", err)
	}
}
