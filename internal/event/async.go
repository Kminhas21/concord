package event

import (
	"context"
	"sync"
	"time"
)

// defaultPublishTimeout bounds a single Sink.Publish so a wedged transport
// cannot stall the drain goroutine forever.
const defaultPublishTimeout = 5 * time.Second

// Sink is the blocking backend an AsyncEmitter drains events to (NATS JetStream
// in production, a fake in tests). Unlike Emitter, Publish may block and return
// an error — the AsyncEmitter absorbs both off the coordination path.
type Sink interface {
	Publish(ctx context.Context, e Event) error
}

// AsyncEmitter is the best-effort Emitter (ADR-0009). Emit hands the event to a
// bounded buffer and returns immediately; a background goroutine drains the
// buffer to a Sink. When the buffer is full, or a Publish errors, the event is
// dropped and onDrop is called — Emit never blocks and never fails. Emit is safe
// for concurrent use and remains panic-free after Close (a late event drops).
type AsyncEmitter struct {
	ch             chan Event
	onDrop         func()
	done           chan struct{}
	publishTimeout time.Duration

	mu     sync.Mutex // guards closed and the send on ch, so Emit never races Close
	closed bool
}

// NewAsyncEmitter starts a drain goroutine publishing to sink. buf is the buffer
// depth; onDrop (may be nil) is invoked once per dropped event and must be safe
// for concurrent use — it is called from both the calling goroutine (full
// buffer) and the drain goroutine (publish error). Call Close to stop the
// goroutine and flush the buffer.
func NewAsyncEmitter(sink Sink, buf int, onDrop func()) *AsyncEmitter {
	if onDrop == nil {
		onDrop = func() {}
	}
	a := &AsyncEmitter{
		ch:             make(chan Event, buf),
		onDrop:         onDrop,
		done:           make(chan struct{}),
		publishTimeout: defaultPublishTimeout,
	}
	go a.drain(sink)
	return a
}

var _ Emitter = (*AsyncEmitter)(nil)

// Emit enqueues e without blocking. If the buffer is full, or the emitter is
// closed, the event is dropped (onDrop is called) — never a panic, never a block.
func (a *AsyncEmitter) Emit(e Event) {
	a.mu.Lock()
	if !a.closed {
		select {
		case a.ch <- e: // non-blocking: buffered channel with a default case
			a.mu.Unlock()
			return
		default:
		}
	}
	a.mu.Unlock()
	a.onDrop() // buffer full, or already closed
}

// drain publishes buffered events to sink until the channel is closed.
func (a *AsyncEmitter) drain(sink Sink) {
	defer close(a.done)
	for e := range a.ch {
		ctx, cancel := context.WithTimeout(context.Background(), a.publishTimeout)
		if err := sink.Publish(ctx, e); err != nil {
			a.onDrop()
		}
		cancel()
	}
}

// Close stops accepting events and waits for the buffer to drain. It is
// idempotent and safe to call concurrently with Emit.
func (a *AsyncEmitter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	close(a.ch) // safe: Emit only sends under mu, and never after closed is set
	a.mu.Unlock()
	<-a.done
	return nil
}
