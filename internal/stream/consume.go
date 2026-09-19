package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Kminhas21/concord/internal/event"
)

// Consumer subscribes to the concord event stream and delivers each event to a
// handler. It is the consumer side used by event-web; the daemon never consumes.
type Consumer struct {
	nc *nats.Conn
	cc jetstream.ConsumeContext
}

// Consume connects to NATS, ensures the stream exists, and starts delivering
// every event on the stream to handle — replaying the stored history first
// (DeliverAll), so a freshly (re)started consumer repopulates from the durable
// stream, then streaming live. handle is called from the consumer's goroutine
// and must not block for long. Call Close to stop.
func Consume(ctx context.Context, url string, handle func(event.Event)) (*Consumer, error) {
	nc, js, err := connect(ctx, url)
	if err != nil {
		return nil, err
	}
	s, err := js.Stream(ctx, StreamName)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("get stream: %w", err)
	}
	cons, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		AckPolicy:     jetstream.AckNonePolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		// Ephemeral (no Durable): a restarted event-web replays from the stream,
		// and the server ages the abandoned consumer out after inactivity so
		// restarts don't accumulate consumers server-side.
		InactiveThreshold: 5 * time.Minute,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var e event.Event
		if err := json.Unmarshal(msg.Data(), &e); err == nil {
			handle(e)
		}
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("start consume: %w", err)
	}
	return &Consumer{nc: nc, cc: cc}, nil
}

// Close stops consuming and drains the connection.
func (c *Consumer) Close() error {
	c.cc.Stop()
	return c.nc.Drain()
}
