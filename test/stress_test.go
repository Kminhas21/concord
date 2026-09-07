//go:build stress

// Stress / failure-hunting harness. Not part of the normal gate.
// Run: go test -tags stress ./test/ -v -run TestStress -timeout 300s
// It reports FINDING (a real weakness) or OK per probe; it does not fail the
// build, so it reads as a report.
package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/hook"
	"github.com/Kminhas21/concord/internal/store"
)

func stressClient(t *testing.T) concordv1connect.CoordinationServiceClient {
	t.Helper()
	addr := StartDragonfly(t)
	rs := store.NewRedisStore(addr, 10*time.Minute)
	svc := coordination.NewService(rs, rs)
	mux := http.NewServeMux()
	p, h := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)
}

func sQuery(t *testing.T, c concordv1connect.CoordinationServiceClient, actor string, paths ...string) *concordv1.IntentMatch {
	resp, err := c.QueryIntent(context.Background(), connect.NewRequest(&concordv1.QueryIntentRequest{Paths: paths}))
	if err != nil {
		t.Fatalf("QueryIntent: %v", err)
	}
	for _, m := range resp.Msg.GetMatches() {
		if m.GetActorId() == actor {
			return m
		}
	}
	return nil
}

func TestStress(t *testing.T) {
	ctx := context.Background()

	// A — concurrent AppendActual on one actor. AppendActual is GET+SET, not
	// atomic, so concurrent touches may lose paths.
	t.Run("A_concurrent_append_actual_lost_paths", func(t *testing.T) {
		c := stressClient(t)
		const N = 100
		var wg sync.WaitGroup
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _ = c.AppendActual(ctx, connect.NewRequest(&concordv1.AppendActualRequest{
					ActorId: "stress-A", Path: fmt.Sprintf("area/f%d.go", i),
				}))
			}(i)
		}
		wg.Wait()
		m := sQuery(t, c, "stress-A", "area/f0.go")
		got := 0
		if m != nil {
			got = len(m.GetPaths())
		}
		if got < N {
			t.Logf("FINDING A: concurrent AppendActual kept %d of %d paths — non-atomic GET+SET loses writes under contention", got, N)
		} else {
			t.Logf("OK A: all %d concurrent paths retained", N)
		}
	})

	// B — predicted comes from a prompt (repo-relative); actual/query comes from
	// hooks (absolute). After the fix the client canonicalizes both to
	// repo-relative, so they overlap for the same file. This probe applies that
	// canonicalization to the query path, as the hook client now does.
	t.Run("B_relative_predicted_vs_absolute_query", func(t *testing.T) {
		c := stressClient(t)
		_, _ = c.RegisterPredicted(ctx, connect.NewRequest(&concordv1.RegisterPredictedRequest{
			ActorId: "stress-B", IntentText: "refactor auth", PredictedPaths: []string{"src/auth/login.go"},
		}))
		qpath := hook.RepoRelative("/repo", "/repo/src/auth/login.go") // client canonicalization
		m := sQuery(t, c, "stress-B", qpath)
		if m == nil || !m.GetPathOverlap() {
			t.Logf("FINDING B: predicted (relative) does NOT overlap query %q — path-scale mismatch", qpath)
		} else {
			t.Logf("OK B: canonicalization aligns predicted(relative) and query — overlap matched")
		}
	})

	// D — case-only path difference names one file on Windows/macOS. After the
	// fix the client case-folds keys on those platforms; this probe applies that
	// fold, as the hook client now does.
	t.Run("D_case_insensitive_fs", func(t *testing.T) {
		c := stressClient(t)
		readKey := hook.FoldCase("C:/Repo/Foo.txt", true) // client fold on case-insensitive FS
		editKey := hook.FoldCase("C:/Repo/foo.txt", true)
		_, _ = c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{
			ActorId: "stress-D", Path: readKey, Hash: "h1",
		}))
		resp, err := c.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
			ActorId: "stress-D", Path: editKey, CurrentHash: "h1",
		}))
		if err != nil {
			t.Fatalf("CheckEdit: %v", err)
		}
		if !resp.Msg.GetAllowed() {
			t.Logf("FINDING D: read of Foo.txt does not satisfy edit of foo.txt — case-sensitive keys. msg=%q", resp.Msg.GetMessage())
		} else {
			t.Logf("OK D: case-folded keys match")
		}
	})

	// F — latency and error rate under concurrent CheckEdit load.
	t.Run("F_latency_under_load", func(t *testing.T) {
		c := stressClient(t)
		_, _ = c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{ActorId: "stress-F", Path: "f.go", Hash: "h"}))
		const N = 1000
		const conc = 64
		lat := make([]time.Duration, N)
		var errs int64
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		start := time.Now()
		for i := 0; i < N; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				s := time.Now()
				_, err := c.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{ActorId: "stress-F", Path: "f.go", CurrentHash: "h"}))
				lat[i] = time.Since(s)
				if err != nil {
					atomic.AddInt64(&errs, 1)
				}
			}(i)
		}
		wg.Wait()
		total := time.Since(start)
		sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
		p := func(q float64) time.Duration { return lat[int(float64(N-1)*q)] }
		t.Logf("F: %d CheckEdit @ conc=%d in %s (%.0f rps) | p50=%s p99=%s max=%s errors=%d",
			N, conc, total.Round(time.Millisecond), float64(N)/total.Seconds(), p(.5), p(.99), lat[N-1], errs)
		if errs > 0 {
			t.Logf("FINDING F: %d/%d CheckEdit calls errored under load", errs, N)
		}
	})

	// G — TTL expiry mid-think: an agent that pauses longer than the silence
	// window loses its intent record, so dedup goes blind for its remaining work.
	t.Run("G_ttl_expiry_mid_think", func(t *testing.T) {
		addr := StartDragonfly(t)
		rs := store.NewRedisStore(addr, 700*time.Millisecond)
		svc := coordination.NewService(rs, rs)
		mux := http.NewServeMux()
		p, h := concordv1connect.NewCoordinationServiceHandler(svc)
		mux.Handle(p, h)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		c := concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)
		_, _ = c.RegisterPredicted(ctx, connect.NewRequest(&concordv1.RegisterPredictedRequest{ActorId: "stress-G", IntentText: "long task", PredictedPaths: []string{"g/x.go"}}))
		time.Sleep(1 * time.Second) // "thinking" longer than the window
		if sQuery(t, c, "stress-G", "g/x.go") == nil {
			t.Logf("FINDING G: an agent still working but silent for > TTL disappears from the registry — a genuinely-active agent stops deduplicating. TTL cannot distinguish 'thinking' from 'dead'.")
		} else {
			t.Logf("OK G: record survived")
		}
	})
}
