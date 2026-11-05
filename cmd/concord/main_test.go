package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/test"
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

// startHealthDaemon boots run() against a real Dragonfly and returns the RPC
// base URL plus a func that terminates Dragonfly (to simulate it going away).
func startHealthDaemon(t *testing.T, metricsLn net.Listener) (base string, stopDragonfly func()) {
	t.Helper()
	return startDaemon(t, metricsLn, "")
}

// startDaemon is startHealthDaemon with a team token (empty = auth off).
func startDaemon(t *testing.T, metricsLn net.Listener, token string) (base string, stopDragonfly func()) {
	t.Helper()
	dfAddr, terminate, err := test.StartDragonflyCtx(context.Background())
	if err != nil {
		t.Fatalf("start Dragonfly (is Docker running?): %v", err)
	}
	var once sync.Once
	stop := func() { once.Do(terminate) }
	t.Cleanup(stop)

	ln := freeTCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, ln, metricsLn, config{addr: ln.Addr().String(), dragonflyAddr: dfAddr, intentTTL: time.Minute, token: token})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return "http://" + ln.Addr().String(), stop
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never answered: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHealthzAndReadyzOKWithDragonflyUp(t *testing.T) {
	base, _ := startHealthDaemon(t, nil) // metrics disabled: endpoints live on the RPC port
	if got := getStatus(t, base+"/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", got)
	}
	if got := getStatus(t, base+"/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", got)
	}
}

func TestReadyzFailsWhenDragonflyDownButHealthzStaysUp(t *testing.T) {
	base, stopDragonfly := startHealthDaemon(t, freeTCP(t))
	if got := getStatus(t, base+"/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 before stop", got)
	}
	stopDragonfly()
	if got := getStatus(t, base+"/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503 with Dragonfly down", got)
	}
	if got := getStatus(t, base+"/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 (liveness must not depend on Dragonfly)", got)
	}
}

// authedReq builds a request with the given Authorization header ("" = none).
func authedReq[T any](msg *T, authz string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if authz != "" {
		r.Header().Set("Authorization", authz)
	}
	return r
}

func TestTokenSetRejectsMissingWrongAndUnprefixed(t *testing.T) {
	base, _ := startDaemon(t, nil, "s3cret")
	getStatus(t, base+"/healthz") // wait for readiness
	c := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, base)
	for name, authz := range map[string]string{
		"none": "", "wrong": "Bearer nope", "no prefix": "s3cret", "wrong scheme": "Basic s3cret",
	} {
		_, err := c.Ping(context.Background(), authedReq(&concordv1.PingRequest{Message: "x"}, authz))
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: code = %v, want Unauthenticated (err=%v)", name, connect.CodeOf(err), err)
		}
	}
}

func TestTokenSetRightTokenKeepsVersionCheck(t *testing.T) {
	base, _ := startDaemon(t, nil, "s3cret")
	getStatus(t, base+"/healthz")
	c := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, base)
	ctx, ok := context.Background(), "Bearer s3cret"
	if _, err := c.RecordRead(ctx, authedReq(&concordv1.RecordReadRequest{ActorId: "a", Path: "x.go", Hash: "h1"}, ok)); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	stale, err := c.CheckEdit(ctx, authedReq(&concordv1.CheckEditRequest{ActorId: "a", Path: "x.go", CurrentHash: "h2"}, ok))
	if err != nil || stale.Msg.Allowed {
		t.Fatalf("stale CheckEdit = %v, %v; want blocked", stale, err)
	}
	match, err := c.CheckEdit(ctx, authedReq(&concordv1.CheckEditRequest{ActorId: "a", Path: "x.go", CurrentHash: "h1"}, ok))
	if err != nil || !match.Msg.Allowed {
		t.Fatalf("matching CheckEdit = %v, %v; want allowed", match, err)
	}
}

func TestTokenUnsetIgnoresAuthorizationHeader(t *testing.T) {
	base, _ := startDaemon(t, nil, "")
	getStatus(t, base+"/healthz")
	c := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, base)
	for _, authz := range []string{"", "Bearer anything", "garbage"} {
		if _, err := c.Ping(context.Background(), authedReq(&concordv1.PingRequest{Message: "x"}, authz)); err != nil {
			t.Errorf("authz %q: %v", authz, err)
		}
	}
}

func TestTokenSetProbesNeedNoHeader(t *testing.T) {
	base, _ := startDaemon(t, nil, "s3cret")
	for _, p := range []string{"/healthz", "/readyz"} {
		if got := getStatus(t, base+p); got != http.StatusOK {
			t.Errorf("%s = %d, want 200 without a token", p, got)
		}
	}
}

func TestTokenSetRejectionIncrementsAuthRejectedMetric(t *testing.T) {
	metricsLn := freeTCP(t)
	mAddr := metricsLn.Addr().String()
	base, _ := startDaemon(t, metricsLn, "s3cret")
	getStatus(t, base+"/healthz")
	c := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, base)
	if _, err := c.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
	resp, err := http.Get("http://" + mAddr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !regexp.MustCompile(`(?m)^concord_auth_rejected_total\{[^}]*\} 1$`).Match(b) {
		t.Fatalf("/metrics missing concord_auth_rejected_total 1; body=%s", b)
	}
}
