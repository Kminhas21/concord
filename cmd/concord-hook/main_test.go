package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/hashing"
	"github.com/Kminhas21/concord/internal/store"
	"github.com/Kminhas21/concord/test"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	h, exists, err := hashing.HashFile(path)
	if err != nil || !exists {
		t.Fatalf("hash %s: exists=%v err=%v", path, exists, err)
	}
	return h
}

// TestShellReconcileAttributesByContentDelta exercises the whole cross-process
// snapshot mechanism (ADR-0008, T12) end to end: snapshotDirtyState writes the
// before-command hashes to a temp file, and reconcileShellWrites reads that file
// back and attributes writes by content delta. It proves the US3 protection —
// a file the human dirtied out of band and the command left untouched is NOT
// reconciled — while the command's own write IS. This is the load-bearing disk
// path that the pure ReconcileTargets unit test does not cover.
func TestShellReconcileAttributesByContentDelta(t *testing.T) {
	// In-process daemon backed by a real Dragonfly; point the hook client at it.
	addr := test.StartDragonfly(t)
	rs := store.NewRedisStore(addr, time.Minute)
	svc := coordination.NewService(rs, rs)
	mux := http.NewServeMux()
	p, h := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("CONCORD_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	c := newClient()

	// A git working tree, entered so repoRoot() resolves to it.
	dir := t.TempDir()
	t.Chdir(dir)
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "t@t")
	mustGit(t, dir, "config", "user.name", "t")
	if err := os.WriteFile("foo.go", []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("bar.go", []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-qm", "init")

	ctx := context.Background()
	const actor = "it-actor"

	// A read foo at v1.
	if _, err := c.RecordRead(ctx, connect.NewRequest(&concordv1.RecordReadRequest{
		ActorId: actor, Path: canonical("foo.go"), Hash: hashOf(t, "foo.go"),
	})); err != nil {
		t.Fatal(err)
	}

	// The human edits foo out of band (v2) before the shell command runs.
	if err := os.WriteFile("foo.go", []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A's shell command: snapshot the dirty tree (foo already dirty at v2), then
	// the command writes only bar.go. Reconcile must attribute only bar.
	snapshotDirtyState(ctx, actor)
	if err := os.WriteFile("bar.go", []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	reconcileShellWrites(ctx, c, actor)

	// foo was the human's change, excluded from reconcile, so A's read-hash is
	// still v1 and its edit of foo must be BLOCKED (the US3 protection).
	fooResp, err := c.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId: actor, Path: canonical("foo.go"), CurrentHash: hashOf(t, "foo.go"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if fooResp.Msg.GetAllowed() {
		t.Fatal("US3 clobber: A's edit of the human-dirtied foo.go was allowed; reconcile misattributed it")
	}

	// bar was the command's own write, reconciled, so A's edit of bar is ALLOWED.
	barResp, err := c.CheckEdit(ctx, connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId: actor, Path: canonical("bar.go"), CurrentHash: hashOf(t, "bar.go"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !barResp.Msg.GetAllowed() {
		t.Fatalf("A's edit of its own shell-written bar.go was blocked: %q", barResp.Msg.GetMessage())
	}
}
