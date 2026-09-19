// Command smoke is the acceptance gate for the observability compose stack
// (ticket 02). It assumes `docker compose ... up` is already running and:
//  1. drives coordination traffic at the concord daemon (a stale CheckEdit and
//     an overlapping intent query) so the metrics move,
//  2. asserts the daemon exposes /metrics,
//  3. asserts Prometheus scrapes concord (target up) and has the series,
//  4. asserts the provisioned Grafana dashboard exists.
//
// It exits non-zero with a clear message on the first failed assertion, so
// `make obs-smoke` fails loudly. All checks poll with a timeout to absorb
// container start-up and scrape latency.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
)

// Host ports published by deploy/observability/docker-compose.yml.
const (
	concordRPC     = "http://localhost:8080"
	concordMetrics = "http://localhost:9464/metrics"
	promBase       = "http://localhost:9090"
	grafanaBase    = "http://admin:admin@localhost:3000"
	dashboardUID   = "concord-overview"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nobs-smoke FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nobs-smoke PASSED: metrics flow daemon -> Prometheus -> Grafana")
}

func run() error {
	// The daemon is the first thing to come up; wait for it, then drive traffic.
	client := concordv1connect.NewCoordinationServiceClient(http.DefaultClient, concordRPC)
	if err := waitFor("concord daemon", 60*time.Second, func() error {
		_, err := client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "smoke"}))
		return err
	}); err != nil {
		return err
	}
	// Retry: Ping does not touch the store, and compose depends_on waits for
	// container start, not Dragonfly readiness — so the first RecordRead can race
	// a not-yet-accepting Dragonfly. Poll until the traffic lands.
	if err := waitFor("drive coordination traffic", 30*time.Second, func() error {
		return driveTraffic(client)
	}); err != nil {
		return err
	}

	// 1. The daemon's own /metrics surface.
	if err := waitFor("concord /metrics", 30*time.Second, func() error {
		body, err := httpGet(concordMetrics)
		if err != nil {
			return err
		}
		if !strings.Contains(body, "concord_checkedit_total") {
			return fmt.Errorf("/metrics missing concord_checkedit_total")
		}
		return nil
	}); err != nil {
		return err
	}

	// 2. Prometheus is scraping concord (target up == 1).
	if err := waitFor("prometheus target up", 60*time.Second, func() error {
		v, err := promScalar(`up{job="concord"}`)
		if err != nil {
			return err
		}
		if v != "1" {
			return fmt.Errorf(`up{job="concord"} = %q, want "1"`, v)
		}
		return nil
	}); err != nil {
		return err
	}

	// 3. The driven decision reached Prometheus.
	if err := waitFor("prometheus has blocked_stale", 30*time.Second, func() error {
		v, err := promScalar(`concord_checkedit_total{decision="blocked_stale"}`)
		if err != nil {
			return err
		}
		if v == "" || v == "0" {
			return fmt.Errorf("concord_checkedit_total{decision=blocked_stale} = %q, want > 0", v)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := waitFor("prometheus has overlaps", 30*time.Second, func() error {
		v, err := promScalar(`concord_overlaps_total`)
		if err != nil {
			return err
		}
		if v == "" || v == "0" {
			return fmt.Errorf("concord_overlaps_total = %q, want > 0", v)
		}
		return nil
	}); err != nil {
		return err
	}

	// 4. The provisioned Grafana dashboard exists.
	if err := waitFor("grafana dashboard provisioned", 60*time.Second, func() error {
		body, err := httpGet(grafanaBase + "/api/dashboards/uid/" + dashboardUID)
		if err != nil {
			return err
		}
		if !strings.Contains(body, dashboardUID) {
			return fmt.Errorf("dashboard %q not found via Grafana API", dashboardUID)
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// driveTraffic makes the daemon record a stale block and an overlap so the
// counters move to non-zero.
func driveTraffic(c concordv1connect.CoordinationServiceClient) error {
	ctx := context.Background()
	const actor, path = "smoke", "src/auth/login.go"
	if _, err := c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{ActorId: actor, Path: path, Hash: "v1"})); err != nil {
		return err
	}
	// v2 != recorded v1 -> blocked_stale.
	if _, err := c.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{ActorId: actor, Path: path, CurrentHash: "v2"})); err != nil {
		return err
	}
	// Two actors predicting the same path -> a query surfaces an overlap.
	for _, a := range []string{"smoke-a", "smoke-b"} {
		if _, err := c.RegisterPredicted(ctx, connect.NewRequest(&concordv1.RegisterPredictedRequest{ActorId: a, IntentText: "auth work", PredictedPaths: []string{path}})); err != nil {
			return err
		}
	}
	if _, err := c.QueryIntent(ctx, connect.NewRequest(&concordv1.QueryIntentRequest{Paths: []string{path}})); err != nil {
		return err
	}
	return nil
}

// promScalar runs an instant PromQL query and returns the first sample's value,
// or "" when the result set is empty.
func promScalar(query string) (string, error) {
	body, err := httpGet(promBase + "/api/v1/query?query=" + url.QueryEscape(query))
	if err != nil {
		return "", err
	}
	var out struct {
		Data struct {
			// Each result's value is [<unix ts float>, "<sample value string>"].
			Result []struct {
				Value [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return "", fmt.Errorf("parse prometheus response: %w", err)
	}
	if len(out.Data.Result) == 0 {
		return "", nil // empty result set
	}
	var v string
	if err := json.Unmarshal(out.Data.Result[0].Value[1], &v); err != nil {
		return "", fmt.Errorf("parse prometheus value: %w", err)
	}
	return v, nil
}

func httpGet(url string) (string, error) {
	resp, err := http.Get(url) //nolint:gosec // fixed localhost URLs in a smoke test
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s -> %d: %s", url, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// waitFor polls check until it returns nil or timeout elapses, printing a single
// progress line per stage. It returns the last error on timeout.
func waitFor(stage string, timeout time.Duration, check func() error) error {
	fmt.Printf("  waiting: %-32s", stage)
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if last = check(); last == nil {
			fmt.Println("ok")
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Println("timeout")
			return fmt.Errorf("%s: %w", stage, last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
