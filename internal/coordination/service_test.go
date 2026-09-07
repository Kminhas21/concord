package coordination_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
	"github.com/Kminhas21/concord/internal/store"
	"github.com/Kminhas21/concord/test"
)

// dragonflyAddr is the address of the one ephemeral Dragonfly shared by every
// test in this package (booted once in TestMain). Tests isolate themselves by
// using unique actor ids rather than flushing between runs.
var dragonflyAddr string

func TestMain(m *testing.M) {
	ctx := context.Background()
	addr, terminate, err := test.StartDragonflyCtx(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Dragonfly (is the Docker daemon running?):", err)
		os.Exit(1)
	}
	dragonflyAddr = addr
	code := m.Run()
	terminate()
	os.Exit(code)
}

// newTestClient wires a Service backed by the shared Dragonfly onto an httptest
// server and returns a Connect client for it. This is the seam. Intent records
// get a long TTL so unrelated tests never expire mid-run.
func newTestClient(t *testing.T) concordv1connect.CoordinationServiceClient {
	return newTestClientTTL(t, 10*time.Minute)
}

// newTestClientTTL is newTestClient with a chosen intent TTL, for tests that
// exercise expiry.
func newTestClientTTL(t *testing.T, intentTTL time.Duration) concordv1connect.CoordinationServiceClient {
	t.Helper()
	rs := store.NewRedisStore(dragonflyAddr, intentTTL)
	svc := coordination.NewService(rs, rs)
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(svc)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return concordv1connect.NewCoordinationServiceClient(srv.Client(), srv.URL)
}

func TestPingEchoesMessage(t *testing.T) {
	client := newTestClient(t)

	resp, err := client.Ping(context.Background(), connect.NewRequest(&concordv1.PingRequest{Message: "concord"}))
	if err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if got := resp.Msg.GetMessage(); got != "concord" {
		t.Fatalf("Ping message = %q, want %q", got, "concord")
	}
}

// recordRead is a small helper to register an actor's read-hash for a path.
func recordRead(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, path, hash string) {
	t.Helper()
	_, err := c.RecordRead(context.Background(), connect.NewRequest(&concordv1.RecordReadRequest{
		ActorId: actor, Path: path, Hash: hash,
	}))
	if err != nil {
		t.Fatalf("RecordRead(%s,%s): %v", actor, path, err)
	}
}

func checkEdit(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, path, current string) *concordv1.CheckEditResponse {
	t.Helper()
	resp, err := c.CheckEdit(context.Background(), connect.NewRequest(&concordv1.CheckEditRequest{
		ActorId: actor, Path: path, CurrentHash: current,
	}))
	if err != nil {
		t.Fatalf("CheckEdit(%s,%s): %v", actor, path, err)
	}
	return resp.Msg
}

func TestCheckEditAllowsWhenHashUnchanged(t *testing.T) {
	c := newTestClient(t)
	recordRead(t, c, "actor-unchanged", "a.go", "hash-1")

	got := checkEdit(t, c, "actor-unchanged", "a.go", "hash-1")

	if !got.GetAllowed() {
		t.Fatalf("edit blocked on unchanged file: %q", got.GetMessage())
	}
}

func TestCheckEditBlocksWhenHashChanged(t *testing.T) {
	c := newTestClient(t)
	recordRead(t, c, "actor-changed", "a.go", "hash-1")

	got := checkEdit(t, c, "actor-changed", "a.go", "hash-2")

	if got.GetAllowed() {
		t.Fatal("edit allowed on a file that changed since the recorded read")
	}
	if got.GetMessage() == "" {
		t.Fatal("blocked edit carried no guidance message")
	}
}

func TestCheckEditAllowsNewFileWithNoRecordedRead(t *testing.T) {
	c := newTestClient(t)

	// No RecordRead, and current_hash empty => the file does not exist => creation.
	got := checkEdit(t, c, "actor-newfile", "new.go", "")

	if !got.GetAllowed() {
		t.Fatalf("creation of a new file blocked: %q", got.GetMessage())
	}
}

func TestCheckEditBlocksExistingFileWithNoRecordedRead(t *testing.T) {
	c := newTestClient(t)

	// No RecordRead but the file exists on disk (non-empty current hash).
	got := checkEdit(t, c, "actor-noread", "exists.go", "hash-x")

	if got.GetAllowed() {
		t.Fatal("edit allowed on an existing file the actor never read")
	}
}

func registerPredicted(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, intent string, paths ...string) {
	t.Helper()
	_, err := c.RegisterPredicted(context.Background(), connect.NewRequest(&concordv1.RegisterPredictedRequest{
		ActorId: actor, IntentText: intent, PredictedPaths: paths,
	}))
	if err != nil {
		t.Fatalf("RegisterPredicted(%s): %v", actor, err)
	}
}

func queryIntent(t *testing.T, c concordv1connect.CoordinationServiceClient, intent string, paths ...string) []*concordv1.IntentMatch {
	t.Helper()
	resp, err := c.QueryIntent(context.Background(), connect.NewRequest(&concordv1.QueryIntentRequest{
		IntentText: intent, Paths: paths,
	}))
	if err != nil {
		t.Fatalf("QueryIntent: %v", err)
	}
	return resp.Msg.GetMatches()
}

func findMatch(matches []*concordv1.IntentMatch, actor string) *concordv1.IntentMatch {
	for _, m := range matches {
		if m.GetActorId() == actor {
			return m
		}
	}
	return nil
}

func appendActual(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, path string) {
	t.Helper()
	_, err := c.AppendActual(context.Background(), connect.NewRequest(&concordv1.AppendActualRequest{
		ActorId: actor, Path: path,
	}))
	if err != nil {
		t.Fatalf("AppendActual(%s,%s): %v", actor, path, err)
	}
}

func TestAppendActualExtendsFootprint(t *testing.T) {
	c := newTestClient(t)
	registerPredicted(t, c, "agent-actual", "refactor auth", "t7actual/auth/login.go")

	// A path outside the predicted scope, added as the agent actually works.
	appendActual(t, c, "agent-actual", "t7actual/db/schema.go")

	// Querying that new area now overlaps, and the footprint includes it.
	m := findMatch(queryIntent(t, c, "touch schema", "t7actual/db/schema.go"), "agent-actual")
	if m == nil || !m.GetPathOverlap() {
		t.Fatal("actual path not reflected in the footprint / overlap")
	}
	found := false
	for _, p := range m.GetPaths() {
		if p == "t7actual/db/schema.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("appended actual path missing from footprint: %v", m.GetPaths())
	}
}

func TestActualFootprintExpiresOnSilence(t *testing.T) {
	c := newTestClientTTL(t, 800*time.Millisecond)
	registerPredicted(t, c, "agent-expire", "short-lived", "t7exp/x.go")
	appendActual(t, c, "agent-expire", "t7exp/y.go")

	if findMatch(queryIntent(t, c, "q", "t7exp/x.go"), "agent-expire") == nil {
		t.Fatal("precondition: record absent immediately after write")
	}

	time.Sleep(1200 * time.Millisecond) // exceed the silence window

	if findMatch(queryIntent(t, c, "q", "t7exp/x.go"), "agent-expire") != nil {
		t.Fatal("record did not expire after silence beyond the TTL")
	}
}

func TestTouchRefreshesTTL(t *testing.T) {
	c := newTestClientTTL(t, 800*time.Millisecond)
	registerPredicted(t, c, "agent-refresh", "kept alive", "t7ref/x.go")

	time.Sleep(500 * time.Millisecond)
	appendActual(t, c, "agent-refresh", "t7ref/y.go") // touch refreshes the timer
	time.Sleep(500 * time.Millisecond)                // 1s since register, but 500ms since touch

	if findMatch(queryIntent(t, c, "q", "t7ref/x.go"), "agent-refresh") == nil {
		t.Fatal("record expired despite a touch within the silence window")
	}
}

func TestQueryReportsPathOverlapSameDirectory(t *testing.T) {
	c := newTestClient(t)
	registerPredicted(t, c, "agent-overlap", "refactor auth", "t6overlap/auth/login.go")

	// A different file in the same directory must count as a path overlap.
	matches := queryIntent(t, c, "touch logout", "t6overlap/auth/logout.go")

	m := findMatch(matches, "agent-overlap")
	if m == nil {
		t.Fatal("registered agent not returned by QueryIntent")
	}
	if !m.GetPathOverlap() {
		t.Fatalf("expected path overlap for same directory, got false (paths=%v)", m.GetPaths())
	}
	if m.GetIntentText() != "refactor auth" {
		t.Fatalf("intent_text = %q, want %q", m.GetIntentText(), "refactor auth")
	}
}

func TestQueryReturnsCandidateWithoutPathOverlap(t *testing.T) {
	c := newTestClient(t)
	registerPredicted(t, c, "agent-candidate", "refactor auth", "t6cand/auth/login.go")

	// Different directory: no path overlap, but the intent must still be
	// returned as a candidate for the caller's semantic judgement.
	matches := queryIntent(t, c, "work on billing", "t6cand/billing/pay.go")

	m := findMatch(matches, "agent-candidate")
	if m == nil {
		t.Fatal("candidate not returned for semantic judgement")
	}
	if m.GetPathOverlap() {
		t.Fatal("unexpected path overlap across different directories")
	}
}

func TestQueryNeverDeniesOnFullOverlap(t *testing.T) {
	c := newTestClient(t)
	registerPredicted(t, c, "agent-samefile", "edit config", "t6deny/config.go")

	// Even an exact same-file overlap returns a normal response, never an error
	// or a denial — the registry only informs.
	matches := queryIntent(t, c, "edit config", "t6deny/config.go")

	m := findMatch(matches, "agent-samefile")
	if m == nil || !m.GetPathOverlap() {
		t.Fatal("expected agent-samefile reported with path overlap")
	}
}

func reconcile(t *testing.T, c concordv1connect.CoordinationServiceClient, actor, path, newHash string) {
	t.Helper()
	_, err := c.ReconcileFileChange(context.Background(), connect.NewRequest(&concordv1.ReconcileFileChangeRequest{
		ActorId: actor, Path: path, NewHash: newHash,
	}))
	if err != nil {
		t.Fatalf("ReconcileFileChange(%s,%s): %v", actor, path, err)
	}
}

func TestReconcileAllowsWritingActorAfterOwnChange(t *testing.T) {
	c := newTestClient(t)
	recordRead(t, c, "writer", "b.go", "hash-1")

	// The writer would be blocked on hash-2 (its own out-of-band shell write)...
	if got := checkEdit(t, c, "writer", "b.go", "hash-2"); got.GetAllowed() {
		t.Fatal("precondition failed: writer not blocked before reconcile")
	}

	// ...until the change is reconciled to the writer's own read-hash.
	reconcile(t, c, "writer", "b.go", "hash-2")

	if got := checkEdit(t, c, "writer", "b.go", "hash-2"); !got.GetAllowed() {
		t.Fatalf("writer still blocked after reconciling its own change: %q", got.GetMessage())
	}
}

func TestReconcileDoesNotAffectOtherActors(t *testing.T) {
	c := newTestClient(t)
	recordRead(t, c, "mover", "c.go", "hash-1")
	recordRead(t, c, "bystander", "c.go", "hash-1")

	// mover rewrites c.go out of band and reconciles its own read-hash.
	reconcile(t, c, "mover", "c.go", "hash-2")

	if got := checkEdit(t, c, "mover", "c.go", "hash-2"); !got.GetAllowed() {
		t.Fatalf("mover blocked after reconciling its own change: %q", got.GetMessage())
	}
	// bystander still holds the old read-hash and must be blocked on the new content.
	if got := checkEdit(t, c, "bystander", "c.go", "hash-2"); got.GetAllowed() {
		t.Fatal("bystander allowed on new content; reconcile leaked across actors")
	}
}

func TestCheckEditIsolatedPerActor(t *testing.T) {
	c := newTestClient(t)

	// Actor A reads shared.go and is allowed to edit it while unchanged.
	recordRead(t, c, "actorA", "shared.go", "hash-1")
	if got := checkEdit(t, c, "actorA", "shared.go", "hash-1"); !got.GetAllowed() {
		t.Fatalf("actor A blocked on its own unchanged read: %q", got.GetMessage())
	}

	// Actor B never read shared.go. Even presenting the identical current
	// content, B must be blocked: it cannot ride on actor A's read.
	if got := checkEdit(t, c, "actorB", "shared.go", "hash-1"); got.GetAllowed() {
		t.Fatal("actor B's edit was allowed on actor A's read; freshness is not isolated per actor")
	}
}
