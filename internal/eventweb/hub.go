// Package eventweb serves concord's live event feed: a hub that keeps a bounded
// ring of the most recent domain events and fans new ones out to connected
// browsers over Server-Sent Events, plus the static page that renders them. It
// is a consumer-side artifact — a small backend/platform piece, not a front-end
// project.
package eventweb

import (
	"sync"

	"github.com/Kminhas21/concord/internal/event"
)

// subBuffer is the per-subscriber channel depth. A browser that falls this far
// behind drops events rather than back-pressuring the hub (the feed is a live
// view, not a durable log — durability lives in JetStream).
const subBuffer = 256

// Hub keeps the most recent N events and broadcasts new ones to subscribers.
// It is safe for concurrent use: the NATS consumer calls Add while browsers
// subscribe and unsubscribe.
type Hub struct {
	mu   sync.Mutex
	n    int
	ring []event.Event
	subs map[chan event.Event]struct{}
}

// NewHub returns a Hub retaining the most recent n events for replay on connect.
func NewHub(n int) *Hub {
	if n < 1 {
		n = 1
	}
	return &Hub{n: n, subs: make(map[chan event.Event]struct{})}
}

// Add records e in the ring (evicting the oldest past capacity) and broadcasts
// it to every subscriber. A subscriber whose buffer is full drops the event —
// Add never blocks on a slow browser.
func (h *Hub) Add(e event.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) == h.n {
		h.ring = h.ring[1:]
	}
	h.ring = append(h.ring, e)
	for ch := range h.subs {
		select {
		case ch <- e:
		default: // slow subscriber: drop rather than block the hub
		}
	}
}

// Subscribe atomically snapshots the current recent events and registers a new
// subscriber channel, so no event is missed or duplicated across the handoff.
// The returned cancel removes and closes the channel; call it exactly once.
func (h *Hub) Subscribe() (recent []event.Event, ch <-chan event.Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	recent = append([]event.Event(nil), h.ring...)
	c := make(chan event.Event, subBuffer)
	h.subs[c] = struct{}{}
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, c)
			close(c)
			h.mu.Unlock()
		})
	}
	return recent, c, cancel
}

// Recent returns a snapshot of the retained events, oldest first.
func (h *Hub) Recent() []event.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]event.Event(nil), h.ring...)
}
