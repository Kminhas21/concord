package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
)

// TestRunServesThenShutsDownGracefully verifies the daemon serves requests and
// that cancelling its context makes run return (a clean graceful shutdown),
// without relying on OS signal delivery. Ping does not touch the store, so no
// Dragonfly is needed.
func TestRunServesThenShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, ln, config{addr: addr, dragonflyAddr: "127.0.0.1:0", intentTTL: time.Minute})
	}()

	client := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, "http://"+addr)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err = client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "up"}))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never served Ping: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel() // request graceful shutdown

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after context cancel — graceful shutdown hung")
	}

	// After shutdown the port must be free again.
	if _, err := client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "down"})); err == nil {
		t.Fatal("daemon still served Ping after shutdown")
	}
}
