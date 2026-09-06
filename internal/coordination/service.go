// Package coordination implements concord's CoordinationService: the single RPC
// seam through which hook clients and tests drive the version check and the
// intent registry.
package coordination

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
	"github.com/Kminhas21/concord/internal/store"
)

// Service implements concordv1connect.CoordinationServiceHandler.
type Service struct {
	readHashes store.ReadHashStore
}

// NewService constructs a Service backed by the given stores.
func NewService(readHashes store.ReadHashStore) *Service {
	return &Service{readHashes: readHashes}
}

var _ concordv1connect.CoordinationServiceHandler = (*Service)(nil)

// Ping echoes the request message, proving the seam is wired end to end.
func (s *Service) Ping(_ context.Context, req *connect.Request[concordv1.PingRequest]) (*connect.Response[concordv1.PingResponse], error) {
	return connect.NewResponse(&concordv1.PingResponse{Message: req.Msg.GetMessage()}), nil
}

// RecordRead stores the hash an actor observed at its last read of a path.
func (s *Service) RecordRead(ctx context.Context, req *connect.Request[concordv1.RecordReadRequest]) (*connect.Response[concordv1.RecordReadResponse], error) {
	m := req.Msg
	if err := s.readHashes.PutReadHash(ctx, m.GetActorId(), m.GetPath(), m.GetHash()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&concordv1.RecordReadResponse{}), nil
}

// CheckEdit blocks a stale edit: one whose target changed since the actor's
// recorded read. It never reads the file itself — the caller supplies the
// current on-disk hash (empty when the file does not exist).
func (s *Service) CheckEdit(ctx context.Context, req *connect.Request[concordv1.CheckEditRequest]) (*connect.Response[concordv1.CheckEditResponse], error) {
	m := req.Msg
	stored, found, err := s.readHashes.GetReadHash(ctx, m.GetActorId(), m.GetPath())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	resp := &concordv1.CheckEditResponse{}
	switch {
	case !found && m.GetCurrentHash() == "":
		// No recorded read and no file on disk: this edit creates a new file.
		resp.Allowed = true
	case !found:
		// The file exists but the actor never read it: no basis for freshness.
		resp.Allowed = false
		resp.Message = fmt.Sprintf("concord: no recorded read of %s; read it before editing", m.GetPath())
	case m.GetCurrentHash() == stored:
		resp.Allowed = true
	default:
		resp.Allowed = false
		resp.Message = fmt.Sprintf("concord: %s changed since you last read it; re-read and retry", m.GetPath())
	}
	return connect.NewResponse(resp), nil
}
