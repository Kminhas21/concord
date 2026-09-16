package store

// White-box store test. This is a deliberate, documented exception to the
// project's "drive everything through the RPC seam, never touch key layout"
// rule (SPEC Testing Decisions): the behaviour under test — ListIntents
// self-healing the active-id set — has no observable effect at the RPC seam
// (QueryIntent already skips expired ids), so the only way to regress it is to
// assert on the internal set membership. See DECISIONS 2026-09-07 (intents-set
// leak).

import (
	"context"
	"testing"
	"time"

	"github.com/Kminhas21/concord/test"
)

// TestListIntentsEvictsExpiredIDFromActiveSet guards the leak fix: once an
// actor's records have expired, ListIntents must drop its id from the active
// set, not leave it to accumulate forever and be re-scanned on every query.
func TestListIntentsEvictsExpiredIDFromActiveSet(t *testing.T) {
	ctx := context.Background()
	addr := test.StartDragonfly(t)
	rs := NewRedisStore(addr, 1*time.Second) // Dragonfly's minimum TTL granularity
	t.Cleanup(func() { _ = rs.Close() })

	const actor = "leak-actor"
	if err := rs.PutPredicted(ctx, actor, "intent", []string{"a.go"}); err != nil {
		t.Fatalf("PutPredicted: %v", err)
	}

	// Precondition: the id is in the active set right after registering.
	members, err := rs.client.SMembers(ctx, intentSetKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if !contains(members, actor) {
		t.Fatalf("precondition: %q not in active set after PutPredicted: %v", actor, members)
	}

	// Let the record expire, then read the registry once (the self-heal path).
	time.Sleep(1500 * time.Millisecond)
	recs, err := rs.ListIntents(ctx)
	if err != nil {
		t.Fatalf("ListIntents: %v", err)
	}
	for _, r := range recs {
		if r.ActorID == actor {
			t.Fatalf("expired actor still returned by ListIntents: %+v", r)
		}
	}

	// The id must be gone from the active set — not left to accumulate.
	members, err = rs.client.SMembers(ctx, intentSetKey).Result()
	if err != nil {
		t.Fatalf("SMembers after expiry: %v", err)
	}
	if contains(members, actor) {
		t.Fatalf("expired actor %q still in active set — leak not evicted: %v", actor, members)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
