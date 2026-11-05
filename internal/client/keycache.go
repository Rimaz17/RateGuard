package client

import (
	"context"
	"time"

	"github.com/Rimaz17/RateGuard/internal/cache"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// DefaultKeyTTL is how long a cached key lives without an invalidation: the
// safety net for a missed revocation notice (ADR 0012).
const DefaultKeyTTL = 10 * time.Second

// keyLoadTimeout bounds one shared key lookup in Postgres.
const keyLoadTimeout = 5 * time.Second

// KeyCache caches stored keys by prefix in front of a KeyFinder, so that
// authenticating a known key reads no database (ADR 0012). It satisfies
// KeyFinder, so an Authenticator uses it unchanged and still compares
// digests on every request.
//
// Only keys that exist are cached, revoked ones included. A prefix with no
// key is looked up again each time, so made-up keys cannot fill the cache.
// A KeyCache is safe for concurrent use.
type KeyCache struct {
	keys *cache.Cache[domain.StoredKey]
}

var _ KeyFinder = (*KeyCache)(nil)

// NewKeyCache returns an empty KeyCache in front of keys whose entries live
// for ttl. opts configure the underlying cache, such as an observer for
// its hit ratio.
func NewKeyCache(keys KeyFinder, ttl time.Duration, opts ...cache.Option) *KeyCache {
	return &KeyCache{keys: cache.New(ttl, keyLoadTimeout, nil, keys.FindByPrefix, opts...)}
}

// FindByPrefix implements KeyFinder.
func (c *KeyCache) FindByPrefix(ctx context.Context, prefix string) (domain.StoredKey, error) {
	return c.keys.Get(ctx, prefix)
}

// InvalidateClient drops every cached key of the client, for when one of its
// keys is revoked or its status changes.
func (c *KeyCache) InvalidateClient(clientID string) {
	c.keys.InvalidateFunc(func(_ string, k domain.StoredKey) bool { return k.ClientID == clientID })
}

// Flush drops every cached key, for when invalidations may have been missed.
func (c *KeyCache) Flush() {
	c.keys.Flush()
}
