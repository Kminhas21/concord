// Package stream is concord's NATS JetStream transport for domain events. It
// provides the production event.Sink (a Publisher) that the daemon's async
// emitter drains to, publishing each event to a durable stream so a
// late-connecting viewer can replay the recent window. The daemon is a producer
// only; consumption (event-web) lives elsewhere.
package stream

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Kminhas21/concord/internal/event"
)

const (
	// StreamName is the durable JetStream stream concord events live in.
	StreamName = "CONCORD_EVENTS"
	// Subject is the subject all concord domain events are published to.
	Subject = "concord.events"
	// MaxMsgs bounds the stream so replay for late viewers stays a recent window
	// rather than growing without limit (LimitsPolicy retention).
	MaxMsgs = 10000
)

// Publisher publishes domain events to the durable JetStream stream. It
// satisfies event.Sink, so an event.AsyncEmitter drains to it off the
// coordination path.
type Publisher struct {
	nc *nats.Conn
	js jetstream.JetStream
}

var _ event.Sink = (*Publisher)(nil)

// Connect dials NATS at url, ensures the durable stream exists, and returns a
// Publisher. The stream is created-or-updated idempotently, so repeated daemon
// starts converge on the same config.
func Connect(ctx context.Context, url string) (*Publisher, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{Subject},
		Retention: jetstream.LimitsPolicy,
		MaxMsgs:   MaxMsgs,
		Storage:   jetstream.FileStorage,
	}); err != nil {
		nc.Close()
		return nil, fmt.Errorf("ensure stream: %w", err)
	}
	return &Publisher{nc: nc, js: js}, nil
}

// Publish marshals e to JSON and publishes it to the stream subject. It returns
// an error when NATS is unreachable or the publish is not acknowledged; the
// caller (the async emitter) treats that as a dropped event (ADR-0009).
func (p *Publisher) Publish(ctx context.Context, e event.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := p.js.Publish(ctx, Subject, b); err != nil {
		return fmt.Errorf("publish event: %w", err)
	}
	return nil
}

// Close drains in-flight publishes and closes the NATS connection.
func (p *Publisher) Close() error {
	return p.nc.Drain()
}
