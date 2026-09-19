package event_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Kminhas21/concord/internal/event"
)

// captureSink records every event it is asked to publish.
type captureSink struct {
	mu   sync.Mutex
	got  []event.Event
	err  error         // returned from Publish when non-nil
	gate chan struct{} // when non-nil, Publish blocks until it is closed
}

func (s *captureSink) Publish(_ context.Context, e event.Event) error {
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	s.got = append(s.got, e)
	s.mu.Unlock()
	return s.err
}

func (s *captureSink) events() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Event(nil), s.got...)
}

func TestAsyncEmitterDeliversToSink(t *testing.T) {
	sink := &captureSink{}
	em := event.NewAsyncEmitter(sink, 8, nil)

	em.Emit(event.Event{Type: event.TypeEditAllowed, ActorID: "a"})
	em.Emit(event.Event{Type: event.TypeEditBlocked, ActorID: "b"})
	if err := em.Close(); err != nil { // Close flushes the buffer
		t.Fatalf("Close: %v", err)
	}

	got := sink.events()
	if len(got) != 2 || got[0].ActorID != "a" || got[1].ActorID != "b" {
		t.Fatalf("sink received %+v, want the two emitted events in order", got)
	}
}

func TestAsyncEmitterDropsWhenBufferFull(t *testing.T) {
	var drops int64
	var mu sync.Mutex
	onDrop := func() { mu.Lock(); drops++; mu.Unlock() }

	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	sink := &blockingSink{gate: gate, started: started}

	const buf = 2
	em := event.NewAsyncEmitter(sink, buf, onDrop)

	// First event is consumed by the drainer, which then blocks in Publish.
	em.Emit(event.Event{ActorID: "e1"})
	<-started // the drainer is now wedged on Publish; the buffer is empty

	// Fill the buffer, then overflow it: the overflow event must be dropped.
	em.Emit(event.Event{ActorID: "e2"})
	em.Emit(event.Event{ActorID: "e3"})
	em.Emit(event.Event{ActorID: "e4-dropped"})

	mu.Lock()
	d := drops
	mu.Unlock()
	if d != 1 {
		t.Fatalf("drops = %d, want 1 (only the overflow event)", d)
	}

	close(gate) // let the drainer finish
	_ = em.Close()
}

func TestAsyncEmitterDropsOnSinkError(t *testing.T) {
	var drops int64
	var mu sync.Mutex
	sink := &captureSink{err: context.DeadlineExceeded}
	em := event.NewAsyncEmitter(sink, 8, func() { mu.Lock(); drops++; mu.Unlock() })

	const n = 5
	for i := 0; i < n; i++ {
		em.Emit(event.Event{ActorID: "x"})
	}
	_ = em.Close() // drains all n; each Publish errors → n drops

	mu.Lock()
	d := drops
	mu.Unlock()
	if d != n {
		t.Fatalf("drops = %d, want %d (one per failed publish)", d, n)
	}
}

func TestAsyncEmitterEmitNeverBlocks(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	em := event.NewAsyncEmitter(&blockingSink{gate: gate, started: started}, 1, nil)

	em.Emit(event.Event{ActorID: "wedge"})
	<-started // drainer blocked; buffer depth 1

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // far more than the buffer can hold
			em.Emit(event.Event{ActorID: "flood"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked when the buffer was full and the sink wedged")
	}

	close(gate)
	_ = em.Close()
}

// blockingSink blocks in Publish until gate is closed, signalling started once
// when it first enters Publish.
type blockingSink struct {
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (s *blockingSink) Publish(_ context.Context, _ event.Event) error {
	s.once.Do(func() { s.started <- struct{}{} })
	<-s.gate
	return nil
}
