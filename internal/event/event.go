// Package event defines concord's domain event — one structured record per
// coordination decision — and the Emitter seam the coordination service depends
// on to publish them. Emission is observation only and strictly best-effort: an
// Emitter must never block or fail a coordination decision (ADR-0009).
package event

import "time"

// Event types, one per coordination decision.
const (
	TypeReadRecorded       = "read_recorded"
	TypeEditAllowed        = "edit_allowed"
	TypeEditBlocked        = "edit_blocked"
	TypeIntentRegistered   = "intent_registered"
	TypeActualAppended     = "actual_appended"
	TypeOverlapReported    = "overlap_reported"
	TypeDivergenceDetected = "divergence_detected"
	TypeReconciled         = "reconciled"
	TypeIntentExpired      = "intent_expired"
)

// Reason values carried by an edit_blocked event; they mirror the CheckEdit
// decision labels used by the metrics (blocked_stale / blocked_no_read).
const (
	ReasonStale  = "stale"
	ReasonNoRead = "no-read"
)

// Event is the structured record emitted on a coordination decision. It is
// designed to carry forward unchanged into team/hosted mode — do not add
// local-only fields.
type Event struct {
	TS        time.Time         `json:"ts"`
	Type      string            `json:"type"`
	ActorID   string            `json:"actor_id"`
	Paths     []string          `json:"paths,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	LatencyMS float64           `json:"latency_ms,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
}

// Emitter accepts domain events. Implementations MUST be non-blocking: Emit is
// called from a coordination handler after the verdict is computed, and may
// never wait on or fail because of telemetry (ADR-0009).
type Emitter interface {
	Emit(Event)
}

// Nop is the default Emitter: it discards every event, so a coordination service
// wired without a real emitter behaves exactly as before observability existed.
type Nop struct{}

// Emit discards e.
func (Nop) Emit(Event) {}
