package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
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
		done <- run(ctx, ln, nil, config{addr: addr, dragonflyAddr: "127.0.0.1:0", intentTTL: time.Minute})
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

// freeTCP returns a listening TCP socket on a free loopback port. The caller
// owns closing it (run closes it via graceful shutdown).
func freeTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// TestMetricsEndpointServesWhenConfigured proves that with a metrics listener the
// daemon exposes /metrics with concord's stable metric surface, while still
// serving coordination. It needs no Dragonfly: the live-intents gauge callback
// fails best-effort and the scrape still succeeds with the other series.
func TestMetricsEndpointServesWhenConfigured(t *testing.T) {
	ln, metricsLn := freeTCP(t), freeTCP(t)
	addr, metricsAddr := ln.Addr().String(), metricsLn.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, ln, metricsLn, config{addr: addr, dragonflyAddr: "127.0.0.1:0", intentTTL: time.Minute})
	}()

	client := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, "http://"+addr)
	body := ""
	deadline := time.Now().Add(3 * time.Second)
	for {
		// Coordination is up.
		if _, err := client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "up"})); err == nil {
			resp, err := http.Get("http://" + metricsAddr + "/metrics")
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				body = string(b)
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics endpoint never served OK; last body=%q", body)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(body, "concord_events_dropped_total") {
		t.Fatalf("/metrics missing the stable concord surface; body=%q", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after context cancel")
	}
}

// TestMetricsDisabledLeavesNoEndpoint proves that with metrics off, /metrics is
// not served on the coordination port — telemetry never leaks onto the RPC
// surface, and disabling it is a clean no-op.
func TestMetricsDisabledLeavesNoEndpoint(t *testing.T) {
	ln := freeTCP(t)
	addr := ln.Addr().String()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, ln, nil, config{addr: addr, dragonflyAddr: "127.0.0.1:0", intentTTL: time.Minute})
	}()

	client := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, "http://"+addr)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "up"})); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never served Ping")
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := http.Get("http://" + addr + "/metrics")
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("/metrics served on the coordination port with metrics disabled")
		}
	}

	cancel()
	<-done
}
