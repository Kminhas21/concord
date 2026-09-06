// Package store persists concord's in-flight state in Dragonfly (a
// Redis-compatible store). It exposes narrow interfaces so the coordination
// service depends on behaviour, not on the client library.
package store

import (
	"context"
	"errors"

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

// RedisStore is a Dragonfly/Redis-backed ReadHashStore.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore connects to a Dragonfly instance at addr (host:port).
func NewRedisStore(addr string) *RedisStore {
	return &RedisStore{client: redis.NewClient(&redis.Options{Addr: addr})}
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
