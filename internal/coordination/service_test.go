package coordination_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/coordination"
)

// newTestClient stands up the Connect handler on an httptest server and returns
// a client pointed at it. This is the seam: every service test drives concord
// through the generated Connect client, never through internals.
func newTestClient(t *testing.T) concordv1connect.CoordinationServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := concordv1connect.NewCoordinationServiceHandler(coordination.NewService())
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
