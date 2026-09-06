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
	intents    store.IntentStore
}

// NewService constructs a Service backed by the given stores.
func NewService(readHashes store.ReadHashStore, intents store.IntentStore) *Service {
	return &Service{readHashes: readHashes, intents: intents}
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

// ReconcileFileChange advances the calling actor's own read-hash for a path it
// rewrote out of band, so that actor is not falsely blocked on its own change.
// It touches only this actor's record; other actors keep their read-hashes and
// stay protected against the new content.
func (s *Service) ReconcileFileChange(ctx context.Context, req *connect.Request[concordv1.ReconcileFileChangeRequest]) (*connect.Response[concordv1.ReconcileFileChangeResponse], error) {
	m := req.Msg
	if err := s.readHashes.PutReadHash(ctx, m.GetActorId(), m.GetPath(), m.GetNewHash()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&concordv1.ReconcileFileChangeResponse{}), nil
}

// RegisterPredicted records an actor's predicted footprint once at start.
func (s *Service) RegisterPredicted(ctx context.Context, req *connect.Request[concordv1.RegisterPredictedRequest]) (*connect.Response[concordv1.RegisterPredictedResponse], error) {
	m := req.Msg
	if err := s.intents.PutPredicted(ctx, m.GetActorId(), m.GetIntentText(), m.GetPredictedPaths()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&concordv1.RegisterPredictedResponse{}), nil
}

// QueryIntent returns every active intent as a candidate, each flagged with
// whether its footprint literally overlaps the query paths. It never denies:
// semantic judgement is left to the caller, which reads intent_text.
func (s *Service) QueryIntent(ctx context.Context, req *connect.Request[concordv1.QueryIntentRequest]) (*connect.Response[concordv1.QueryIntentResponse], error) {
	records, err := s.intents.ListIntents(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	queryPaths := req.Msg.GetPaths()
	resp := &concordv1.QueryIntentResponse{}
	for _, r := range records {
		footprint := append(append([]string{}, r.PredictedPaths...), r.ActualPaths...)
		resp.Matches = append(resp.Matches, &concordv1.IntentMatch{
			ActorId:     r.ActorID,
			IntentText:  r.IntentText,
			Paths:       footprint,
			PathOverlap: pathsOverlap(queryPaths, footprint),
		})
	}
	return connect.NewResponse(resp), nil
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
