package policy

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cache is a TTL cache in front of a loader, with the three properties ADR
// 0005 asks of the policy cache:
//
//   - Entries expire after ttl, as a safety net for a missed invalidation.
//   - Concurrent misses for one key share a single load (singleflight), so
//     a cold cache under load cannot stampede the database.
//   - An invalidation wins over a load already in flight. Every
//     invalidation bumps epoch; a load stores its result only if no
//     invalidation happened since it started, so a value read just before
//     a change can never be cached after it.
//
// Errors are never cached: the next request retries the load.
type cache[V any] struct {
	ttl         time.Duration
	loadTimeout time.Duration
	now         func() time.Time
	load        func(ctx context.Context, key string) (V, error)

	mu    sync.Mutex
	items map[string]cacheItem[V]
	epoch uint64

	flights singleflight.Group
}

type cacheItem[V any] struct {
	value   V
	expires time.Time
}

func newCache[V any](ttl, loadTimeout time.Duration, now func() time.Time, load func(context.Context, string) (V, error)) *cache[V] {
	return &cache[V]{
		ttl:         ttl,
		loadTimeout: loadTimeout,
		now:         now,
		load:        load,
		items:       make(map[string]cacheItem[V]),
	}
}

// get returns the cached value for key, loading it on a miss. A caller whose
// context ends stops waiting, but the shared load carries on for the others.
func (c *cache[V]) get(ctx context.Context, key string) (V, error) {
	if v, ok := c.lookup(key); ok {
		return v, nil
	}
	// DoChan runs the load on a goroutine owned by singleflight. It ends
	// when the load does, which loadTimeout bounds.
	ch := c.flights.DoChan(key, func() (any, error) { return c.fill(ctx, key) })
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

func (c *cache[V]) lookup(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !c.now().Before(it.expires) {
		delete(c.items, key)
		var zero V
		return zero, false
	}
	return it.value, true
}

// fill loads key and caches the result unless an invalidation happened
// while it was loading.
func (c *cache[V]) fill(ctx context.Context, key string) (V, error) {
	// A caller can miss in lookup just before another flight stores its
	// result, and reach DoChan just after that flight has ended. Checking
	// again here serves it the stored value instead of loading twice.
	c.mu.Lock()
	if it, ok := c.items[key]; ok && c.now().Before(it.expires) {
		c.mu.Unlock()
		return it.value, nil
	}
	start := c.epoch
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
		c.items[key] = cacheItem[V]{value: v, expires: c.now().Add(c.ttl)}
	}
	return v, nil
}

// invalidate drops key. Callers that arrive afterwards start a fresh load
// rather than joining one that began before the change.
func (c *cache[V]) invalidate(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.epoch++
	c.mu.Unlock()
	c.flights.Forget(key)
}

// flush drops every entry, for when invalidations may have been missed.
// Loads already in flight finish but do not store their results.
func (c *cache[V]) flush() {
	c.mu.Lock()
	clear(c.items)
	c.epoch++
	c.mu.Unlock()
}
