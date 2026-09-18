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

// Metrics observes coordination decisions. It is called after a verdict is
// computed and never influences it (ADR-0001). A nil metrics dependency is
// replaced by a no-op, so handlers call it unconditionally.
type Metrics interface {
	// CheckEditDecision records one CheckEdit outcome: "allowed",
	// "blocked_stale", "blocked_no_read", or "allowed_new_file".
	CheckEditDecision(ctx context.Context, decision string)
	// OverlapsReported records n path overlaps surfaced by one intent query.
	OverlapsReported(ctx context.Context, n int)
	// DivergencesDetected records n footprint divergences surfaced by one query.
	DivergencesDetected(ctx context.Context, n int)
}

// Service implements concordv1connect.CoordinationServiceHandler.
type Service struct {
	readHashes store.ReadHashStore
	intents    store.IntentStore
	metrics    Metrics
}

// Option configures a Service.
type Option func(*Service)

// WithMetrics wires observability onto the service. Without it, decisions are
// recorded to a no-op and no metrics are exposed.
func WithMetrics(m Metrics) Option {
	return func(s *Service) {
		if m != nil {
			s.metrics = m
		}
	}
}

// NewService constructs a Service backed by the given stores.
func NewService(readHashes store.ReadHashStore, intents store.IntentStore, opts ...Option) *Service {
	s := &Service{readHashes: readHashes, intents: intents, metrics: noopMetrics{}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// noopMetrics is the default Metrics: it records nothing, so a Service built
// without WithMetrics behaves exactly as before observability existed.
type noopMetrics struct{}

func (noopMetrics) CheckEditDecision(context.Context, string) {}
func (noopMetrics) OverlapsReported(context.Context, int)     {}
func (noopMetrics) DivergencesDetected(context.Context, int)  {}

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
	overlaps, divergences := 0, 0
	for _, r := range records {
		footprint := append(append([]string{}, r.PredictedPaths...), r.ActualPaths...)
		match := &concordv1.IntentMatch{
			ActorId:        r.ActorID,
			IntentText:     r.IntentText,
			Paths:          footprint,
			PathOverlap:    pathsOverlap(queryPaths, footprint),
			DivergentPaths: divergentPaths(r.PredictedPaths, r.ActualPaths),
		}
		if match.PathOverlap {
			overlaps++
		}
		if len(match.DivergentPaths) > 0 {
			divergences++
		}
		resp.Matches = append(resp.Matches, match)
	}
	s.metrics.OverlapsReported(ctx, overlaps)
	s.metrics.DivergencesDetected(ctx, divergences)
	return connect.NewResponse(resp), nil
}

// AppendActual adds a path to an actor's actual footprint and refreshes its
// silence timer.
func (s *Service) AppendActual(ctx context.Context, req *connect.Request[concordv1.AppendActualRequest]) (*connect.Response[concordv1.AppendActualResponse], error) {
	m := req.Msg
	if err := s.intents.AppendActual(ctx, m.GetActorId(), m.GetPath()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&concordv1.AppendActualResponse{}), nil
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
	var decision string
	switch {
	case !found && m.GetCurrentHash() == "":
		// No recorded read and no file on disk: this edit creates a new file.
		resp.Allowed = true
		decision = "allowed_new_file"
	case !found:
		// The file exists but the actor never read it: no basis for freshness.
		resp.Allowed = false
		resp.Message = fmt.Sprintf("concord: no recorded read of %s; read it before editing", m.GetPath())
		decision = "blocked_no_read"
	case m.GetCurrentHash() == stored:
		resp.Allowed = true
		decision = "allowed"
	default:
		resp.Allowed = false
		resp.Message = fmt.Sprintf("concord: %s changed since you last read it; re-read and retry", m.GetPath())
		decision = "blocked_stale"
	}
	s.metrics.CheckEditDecision(ctx, decision)
	return connect.NewResponse(resp), nil
}
