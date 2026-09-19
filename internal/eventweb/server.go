package eventweb

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Kminhas21/concord/internal/event"
)

// heartbeatInterval is how often an idle SSE stream emits a comment ping, to
// keep intermediaries (e.g. an L7 proxy in team mode) from reaping the
// connection. Comments (lines starting ":") are ignored by EventSource.
const heartbeatInterval = 20 * time.Second

//go:embed page.html
var pageHTML []byte

// Handler returns the event-web HTTP handler: the static page at /, the SSE
// stream at /events, and a /healthz probe.
func Handler(h *Hub) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(pageHTML)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/events", h.serveSSE)
	return mux
}

// serveSSE streams events to one browser: on connect it replays the buffered
// recent events, then streams new ones until the client disconnects.
func (h *Hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	recent, ch, cancel := h.Subscribe()
	defer cancel()

	for _, e := range recent {
		writeSSE(w, e)
	}
	flusher.Flush()

	ping := time.NewTicker(heartbeatInterval)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			// Fires on client disconnect and on server shutdown (the daemon
			// cancels the base context), so the handler never lingers.
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, e)
			flusher.Flush()
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// writeSSE writes one event as an SSE message: `data: <json>\n\n`.
func writeSSE(w io.Writer, e event.Event) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
}
