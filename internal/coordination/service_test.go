package coordination_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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
// server and returns a Connect client for it. This is the seam.
func newTestClient(t *testing.T) concordv1connect.CoordinationServiceClient {
	t.Helper()
	svc := coordination.NewService(store.NewRedisStore(dragonflyAddr))
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
