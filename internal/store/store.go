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

func intentKey(actorID string) string { return "intent:" + actorID }

var _ IntentStore = (*RedisStore)(nil)

// getRecord loads an actor's intent record, or (nil, nil) when there is none.
func (s *RedisStore) getRecord(ctx context.Context, actorID string) (*IntentRecord, error) {
	raw, err := s.client.Get(ctx, intentKey(actorID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec IntentRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// saveRecord writes the record with the intent TTL (refresh-on-touch) and keeps
// the actor in the active set.
func (s *RedisStore) saveRecord(ctx context.Context, rec *IntentRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, intentKey(rec.ActorID), b, s.intentTTL)
	pipe.SAdd(ctx, intentSetKey, rec.ActorID)
	_, err = pipe.Exec(ctx)
	return err
}

// PutPredicted stores the predicted footprint and refreshes the silence timer.
func (s *RedisStore) PutPredicted(ctx context.Context, actorID, intentText string, predictedPaths []string) error {
	return s.saveRecord(ctx, &IntentRecord{
		ActorID:        actorID,
		IntentText:     intentText,
		PredictedPaths: predictedPaths,
	})
}

// AppendActual adds path to the actor's actual footprint (deduplicated),
// creating the record if needed, and refreshes the silence timer.
func (s *RedisStore) AppendActual(ctx context.Context, actorID, path string) error {
	rec, err := s.getRecord(ctx, actorID)
	if err != nil {
		return err
	}
	if rec == nil {
		rec = &IntentRecord{ActorID: actorID}
	}
	for _, p := range rec.ActualPaths {
		if p == path {
			return s.saveRecord(ctx, rec) // already present; still refresh TTL
		}
	}
	rec.ActualPaths = append(rec.ActualPaths, path)
	return s.saveRecord(ctx, rec)
}

// ListIntents loads every active intent record. Records whose key has expired
// but whose id lingers in the set are skipped.
func (s *RedisStore) ListIntents(ctx context.Context) ([]IntentRecord, error) {
	ids, err := s.client.SMembers(ctx, intentSetKey).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = intentKey(id)
	}
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]IntentRecord, 0, len(vals))
	for _, v := range vals {
		raw, ok := v.(string)
		if !ok {
			continue // key missing/expired
		}
		var rec IntentRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
