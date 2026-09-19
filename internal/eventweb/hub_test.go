package eventweb_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Kminhas21/concord/internal/event"
	"github.com/Kminhas21/concord/internal/eventweb"
)

func TestRingBufferKeepsMostRecentN(t *testing.T) {
	h := eventweb.NewHub(3)
	for i := 0; i < 5; i++ {
		h.Add(event.Event{Type: event.TypeEditAllowed, ActorID: string(rune('a' + i))})
	}
	recent := h.Recent()
	if len(recent) != 3 {
		t.Fatalf("Recent len = %d, want 3 (bounded)", len(recent))
	}
	if recent[0].ActorID != "c" || recent[2].ActorID != "e" {
		t.Fatalf("Recent = %v, want the last three (c,d,e)", []string{recent[0].ActorID, recent[1].ActorID, recent[2].ActorID})
	}
}

func TestSSEReplaysRecentThenStreamsLive(t *testing.T) {
	h := eventweb.NewHub(10)
	h.Add(event.Event{Type: event.TypeReadRecorded, ActorID: "r1"})
	h.Add(event.Event{Type: event.TypeEditBlocked, ActorID: "r2", Reason: event.ReasonStale})

	srv := httptest.NewServer(eventweb.Handler(h))
	t.Cleanup(srv.Close)

	events, stop := sseClient(t, srv.URL)
	defer stop()

	// The two buffered events arrive first, in order.
	if got := recv(t, events); got.ActorID != "r1" {
		t.Fatalf("first buffered event actor = %q, want r1", got.ActorID)
	}
	if got := recv(t, events); got.ActorID != "r2" || got.Reason != event.ReasonStale {
		t.Fatalf("second buffered event = %+v, want r2/stale", got)
	}

	// A live event added after connect streams through.
	h.Add(event.Event{Type: event.TypeOverlapReported, ActorID: "live1"})
	if got := recv(t, events); got.ActorID != "live1" || got.Type != event.TypeOverlapReported {
		t.Fatalf("live event = %+v, want live1/overlap_reported", got)
	}
}

func TestSSELateConnectGetsBufferedEvents(t *testing.T) {
	h := eventweb.NewHub(10)
	srv := httptest.NewServer(eventweb.Handler(h))
	t.Cleanup(srv.Close)

	// Events happen BEFORE any client connects.
	for i := 0; i < 3; i++ {
		h.Add(event.Event{Type: event.TypeActualAppended, ActorID: "pre" + string(rune('0'+i))})
	}

	events, stop := sseClient(t, srv.URL)
	defer stop()

	for i := 0; i < 3; i++ {
		got := recv(t, events)
		want := "pre" + string(rune('0'+i))
		if got.ActorID != want {
			t.Fatalf("buffered replay[%d] = %q, want %q — a late client must not miss what just happened", i, got.ActorID, want)
		}
	}
}

func TestSSEReconnectResumesFromBuffer(t *testing.T) {
	h := eventweb.NewHub(10)
	srv := httptest.NewServer(eventweb.Handler(h))
	t.Cleanup(srv.Close)

	h.Add(event.Event{Type: event.TypeEditAllowed, ActorID: "e1"})

	events, stop := sseClient(t, srv.URL)
	if got := recv(t, events); got.ActorID != "e1" {
		t.Fatalf("first connect got %q, want e1", got.ActorID)
	}
	stop() // client disconnects

	// A fresh connection replays the buffer again (resumes cleanly).
	events2, stop2 := sseClient(t, srv.URL)
	defer stop2()
	if got := recv(t, events2); got.ActorID != "e1" {
		t.Fatalf("reconnect got %q, want e1 from the buffer", got.ActorID)
	}
}

// sseClient connects to the /events SSE endpoint and returns a channel of parsed
// events plus a stop func.
func sseClient(t *testing.T, base string) (<-chan event.Event, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events", nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("connect SSE: %v", err)
	}
	out := make(chan event.Event, 128)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var e event.Event
			if err := json.Unmarshal([]byte(data), &e); err != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel
}

// recv reads one event or fails on timeout.
func recv(t *testing.T, ch <-chan event.Event) event.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE event")
		return event.Event{}
	}
}
