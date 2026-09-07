// Package store persists concord's in-flight state in Dragonfly (a
// Redis-compatible store). It exposes narrow interfaces so the coordination
// service depends on behaviour, not on the client library.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReadHashStore persists the content hash each actor observed at its last read
// of a path — the basis for the version check.
type ReadHashStore interface {
	// PutReadHash records that actorID last read path with the given hash.
	PutReadHash(ctx context.Context, actorID, path, hash string) error
	// GetReadHash returns the recorded hash for (actorID, path). found is false
	// when the actor has no recorded read of the path.
	GetReadHash(ctx context.Context, actorID, path string) (hash string, found bool, err error)
}

// unitSep separates the actor id from the path in a key so the two can never
// collide regardless of their contents.
const unitSep = "\x1f"

func readHashKey(actorID, path string) string {
	return "readhash:" + actorID + unitSep + path
}

// RedisStore is a Dragonfly/Redis-backed store for both read-hashes and intent
// records.
type RedisStore struct {
	client    *redis.Client
	intentTTL time.Duration
}

// NewRedisStore connects to a Dragonfly instance at addr (host:port). intentTTL
// is the silence window after which an untouched intent record expires;
// read-hashes are never expired.
func NewRedisStore(addr string, intentTTL time.Duration) *RedisStore {
	return &RedisStore{
		client:    redis.NewClient(&redis.Options{Addr: addr}),
		intentTTL: intentTTL,
	}
}

// Close releases the underlying client.
func (s *RedisStore) Close() error { return s.client.Close() }

var _ ReadHashStore = (*RedisStore)(nil)

// PutReadHash stores the read-hash with no expiry: the version check holds
// nothing on a timer (ADR-0002); a stale read-hash only forces a harmless
// re-read by that actor.
func (s *RedisStore) PutReadHash(ctx context.Context, actorID, path, hash string) error {
	return s.client.Set(ctx, readHashKey(actorID, path), hash, 0).Err()
}

// GetReadHash returns the recorded read-hash, or found=false when there is none.
func (s *RedisStore) GetReadHash(ctx context.Context, actorID, path string) (string, bool, error) {
	v, err := s.client.Get(ctx, readHashKey(actorID, path)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// IntentRecord is an actor's advisory footprint: its predicted scope plus the
// paths it has actually touched.
type IntentRecord struct {
	ActorID        string   `json:"actor_id"`
	IntentText     string   `json:"intent_text"`
	PredictedPaths []string `json:"predicted_paths"`
	ActualPaths    []string `json:"actual_paths"`
}

// IntentStore persists advisory intent records and enumerates the active ones.
// Every write refreshes the record's silence timer.
type IntentStore interface {
	// PutPredicted writes an actor's predicted footprint, replacing any existing
	// record for that actor.
	PutPredicted(ctx context.Context, actorID, intentText string, predictedPaths []string) error
	// AppendActual adds a path to the actor's actual footprint, creating the
	// record if none exists yet.
	AppendActual(ctx context.Context, actorID, path string) error
	// ListIntents returns every active intent record.
	ListIntents(ctx context.Context) ([]IntentRecord, error)
}

const intentSetKey = "intents"

// intentKey holds the predicted footprint (JSON); actualKey holds the actual
// footprint as a Redis set, so appends are atomic (SADD) and never race.
func intentKey(actorID string) string { return "intent:" + actorID }
func actualKey(actorID string) string { return "intent:" + actorID + ":actual" }

var _ IntentStore = (*RedisStore)(nil)

// PutPredicted stores the predicted footprint (replacing any prior one) and
// refreshes the silence timer on both the predicted doc and the actual set.
func (s *RedisStore) PutPredicted(ctx context.Context, actorID, intentText string, predictedPaths []string) error {
	b, err := json.Marshal(IntentRecord{ActorID: actorID, IntentText: intentText, PredictedPaths: predictedPaths})
	if err != nil {
		return err
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, intentKey(actorID), b, s.intentTTL)
	pipe.Expire(ctx, actualKey(actorID), s.intentTTL) // no-op if the actor has no actual set yet
	pipe.SAdd(ctx, intentSetKey, actorID)
	_, err = pipe.Exec(ctx)
	return err
}

// AppendActual adds path to the actor's actual footprint and refreshes the
// silence timer. SADD is atomic and set-valued, so concurrent appends neither
// race nor duplicate.
func (s *RedisStore) AppendActual(ctx context.Context, actorID, path string) error {
	pipe := s.client.TxPipeline()
	pipe.SAdd(ctx, actualKey(actorID), path)
	pipe.Expire(ctx, actualKey(actorID), s.intentTTL)
	pipe.Expire(ctx, intentKey(actorID), s.intentTTL) // refresh the predicted doc if present
	pipe.SAdd(ctx, intentSetKey, actorID)
	_, err := pipe.Exec(ctx)
	return err
}

// ListIntents loads every active intent record, combining the predicted doc and
// the actual set. An id lingering in the active set whose keys have both expired
// is skipped.
func (s *RedisStore) ListIntents(ctx context.Context) ([]IntentRecord, error) {
	ids, err := s.client.SMembers(ctx, intentSetKey).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	pipe := s.client.Pipeline()
	gets := make([]*redis.StringCmd, len(ids))
	actuals := make([]*redis.StringSliceCmd, len(ids))
	for i, id := range ids {
		gets[i] = pipe.Get(ctx, intentKey(id))
		actuals[i] = pipe.SMembers(ctx, actualKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err // redis.Nil is expected for a missing predicted doc
	}

	out := make([]IntentRecord, 0, len(ids))
	for i, id := range ids {
		rec := IntentRecord{ActorID: id}
		if raw, err := gets[i].Result(); err == nil {
			_ = json.Unmarshal([]byte(raw), &rec)
		} else if !errors.Is(err, redis.Nil) {
			return nil, err
		}
		rec.ActorID = id
		rec.ActualPaths = actuals[i].Val()
		if len(rec.PredictedPaths) == 0 && len(rec.ActualPaths) == 0 && rec.IntentText == "" {
			continue // both keys gone; stale id in the active set
		}
		out = append(out, rec)
	}
	return out, nil
}
