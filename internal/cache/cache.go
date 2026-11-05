// Package cache is the in-process cache that keeps Postgres off the decision
// hot path: a TTL cache in front of a loader, used for resolved policies
// (ADR 0005) and for API keys (ADR 0012).
package cache

import (
	"context"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Loader reads the value for key from the source of truth.
type Loader[V any] func(ctx context.Context, key string) (V, error)

// Cache is a TTL cache in front of a Loader, with the three properties
// ADR 0005 asks of a hot-path cache:
//
//   - Entries expire after the TTL, as a safety net for a missed
//     invalidation.
//   - Concurrent misses for one key share a single load (singleflight), so
//     a cold cache under load cannot stampede the database.
//   - An invalidation wins over a load already in flight. Every
//     invalidation bumps an epoch; a load stores its result only if no
//     invalidation happened since it started, so a value read just before
//     a change can never be cached after it.
//
// Errors are never cached: the next request retries the load. A Cache is
// safe for concurrent use.
type Cache[V any] struct {
	ttl         time.Duration
	loadTimeout time.Duration
	now         func() time.Time
	load        Loader[V]

	mu    sync.Mutex
	items map[string]item[V]
	epoch uint64

	flights singleflight.Group
}

type item[V any] struct {
	value   V
	expires time.Time
}

// New returns an empty Cache whose entries live for ttl and whose loads are
// each bounded by loadTimeout. now is the clock that expires entries, or
// time.Now when nil; it never touches bucket arithmetic, which runs on
// Redis's clock (ADR 0002).
func New[V any](ttl, loadTimeout time.Duration, now func() time.Time, load Loader[V]) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{
		ttl:         ttl,
		loadTimeout: loadTimeout,
		now:         now,
		load:        load,
		items:       make(map[string]item[V]),
	}
}

// Get returns the cached value for key, loading it on a miss. A caller whose
// context ends stops waiting, but the shared load carries on for the others.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	v, epoch, ok := c.lookup(key)
	if ok {
		return v, nil
	}
	// Loads are shared only between callers that missed in the same epoch.
	// A caller arriving after an invalidation therefore starts a load of
	// its own instead of joining one that may have read the old value.
	flight := key + "\x00" + strconv.FormatUint(epoch, 10)
	// DoChan runs the load on a goroutine owned by singleflight. It ends
	// when the load does, which loadTimeout bounds.
	ch := c.flights.DoChan(flight, func() (any, error) { return c.fill(ctx, key, epoch) })
	select {
	case res := <-ch:
		if res.Err != nil {
			var zero V
			return zero, res.Err
		}
		return res.Val.(V), nil
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}

// lookup returns the live entry for key, or the epoch in which it missed.
func (c *Cache[V]) lookup(key string) (V, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		var zero V
		return zero, c.epoch, false
	}
	if !c.now().Before(it.expires) {
		delete(c.items, key)
		var zero V
		return zero, c.epoch, false
	}
	return it.value, c.epoch, true
}

// fill loads key and caches the result unless an invalidation happened
// since start, the epoch in which the callers missed.
func (c *Cache[V]) fill(ctx context.Context, key string, start uint64) (V, error) {
	// A caller can miss in lookup just before another flight stores its
	// result, and reach DoChan just after that flight has ended. Checking
	// again here serves it the stored value instead of loading twice.
	c.mu.Lock()
	if it, ok := c.items[key]; ok && c.now().Before(it.expires) {
		c.mu.Unlock()
		return it.value, nil
	}
	c.mu.Unlock()

	// The load is shared by every caller waiting on key, so the first
	// caller's cancellation must not fail it for the rest. It keeps the
	// caller's values and gets its own deadline instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
	defer cancel()
	v, err := c.load(ctx, key)
	if err != nil {
		return v, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == start {
		c.items[key] = item[V]{value: v, expires: c.now().Add(c.ttl)}
	}
	return v, nil
}

// Invalidate drops key. Callers that arrive afterwards start a fresh load
// rather than joining one that began before the change.
func (c *Cache[V]) Invalidate(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.epoch++
	c.mu.Unlock()
}

// InvalidateFunc drops every entry for which drop reports true, for a change
// that makes several entries stale at once, such as revoking one of a
// client's keys. Like Invalidate, it also stops loads already in flight from
// storing their results, since their keys and values are not known yet.
// drop runs with the cache locked and must not call back into it.
func (c *Cache[V]) InvalidateFunc(drop func(key string, value V) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, it := range c.items {
		if drop(key, it.value) {
			delete(c.items, key)
		}
	}
	c.epoch++
}

// Flush drops every entry, for when invalidations may have been missed.
// Loads already in flight finish but do not store their results, and
// callers that arrive afterwards do not join them.
func (c *Cache[V]) Flush() {
	c.mu.Lock()
	clear(c.items)
	c.epoch++
	c.mu.Unlock()
}
