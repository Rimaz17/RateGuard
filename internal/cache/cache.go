// Package cache is the in-process cache that keeps Postgres off the decision
// hot path: a TTL cache in front of a loader, used for resolved policies
// (ADR 0005) and for API keys (ADR 0012).
package cache

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Loader reads the value for key from the source of truth.
type Loader[V any] func(ctx context.Context, key string) (V, error)

// staleWait is how long a caller waits for a reload when there is an
// expired value to fall back on. A healthy Postgres reloads in about a
// millisecond; a hanging one would otherwise hold the caller for the whole
// load timeout, once per cache a check walks through. Past staleWait the
// caller gets the previous value and the reload carries on without it.
const staleWait = 100 * time.Millisecond

// staleRetry is how often an entry that is being served stale tries its
// source again. The retry runs in the background, so no caller waits for
// it, and an unreachable source is tried about once a second per key
// rather than once per request.
const staleRetry = time.Second

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
// Errors are never cached: the next request retries the load. With
// WithStale, a reload that fails because the source is unreachable serves
// the last loaded value instead of the error (ADR 0021).
//
// A Cache is safe for concurrent use.
type Cache[V any] struct {
	ttl         time.Duration
	loadTimeout time.Duration
	now         func() time.Time
	load        Loader[V]
	observer    Observer
	stale       staleSettings

	mu    sync.Mutex
	items map[string]item[V]
	epoch uint64

	flights singleflight.Group
}

type item[V any] struct {
	value V

	// expires is when the entry stops being fresh.
	expires time.Time

	// keep is when the entry stops being usable as a stale fallback; it
	// equals expires without WithStale. An expired entry stays in the map
	// until keep, so that a failed reload has something to fall back on.
	keep time.Time

	// failing records that the last reload failed and the value is being
	// served stale; retryAt is when the source is next tried.
	failing bool
	retryAt time.Time
}

// filled is a load's result as the callers sharing it see it.
type filled[V any] struct {
	value V
	stale bool
}

// New returns an empty Cache whose entries live for ttl and whose loads are
// each bounded by loadTimeout. now is the clock that expires entries, or
// time.Now when nil; it never touches bucket arithmetic, which runs on
// Redis's clock (ADR 0002).
func New[V any](ttl, loadTimeout time.Duration, now func() time.Time, load Loader[V], opts ...Option) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	if s.observer == nil {
		s.observer = nopObserver{}
	}
	return &Cache[V]{
		ttl:         ttl,
		loadTimeout: loadTimeout,
		now:         now,
		load:        load,
		observer:    s.observer,
		stale:       s.stale,
		items:       make(map[string]item[V]),
	}
}

// Result is what a Get found.
type Result int

const (
	// Miss means there was no fresh entry and the value came from a load
	// (or the load failed).
	Miss Result = iota

	// Hit means a fresh entry answered.
	Hit

	// Stale means the source could not be reached and the last loaded
	// value answered (ADR 0021).
	Stale
)

// String returns the result as the metrics label it becomes.
func (r Result) String() string {
	switch r {
	case Hit:
		return "hit"
	case Stale:
		return "stale"
	default:
		return "miss"
	}
}

// Observer is told the result of every Get, for the cache hit ratio
// (ADR 0019). It runs on the decision path and must be cheap and
// non-blocking.
type Observer interface {
	// Lookup reports what a Get found. A Get that joins another caller's
	// load is a miss, or stale if that load fell back.
	Lookup(r Result)
}

// Option configures a Cache.
type Option func(*settings)

type settings struct {
	observer Observer
	stale    staleSettings
}

type staleSettings struct {
	maxAge  time.Duration
	answers []error
}

// WithObserver reports every lookup to o.
func WithObserver(o Observer) Option {
	return func(s *settings) { s.observer = o }
}

// WithStale keeps serving an entry for up to maxAge past its TTL while its
// source cannot be reached (ADR 0021). A reload that fails then returns the
// last loaded value instead of the error, and later lookups get that value
// at once while the source is retried in the background. A reload slower
// than staleWait (100ms) is answered with the last value too, while it
// carries on.
//
// answers are the errors that are the source's answer rather than a failure
// to reach it, such as "not found": a load failing with one of them
// (errors.Is) drops the entry and returns the error, so a value the source
// has disowned is never served. Invalidations and flushes drop entries as
// before, so an invalidated value is never served stale either. A maxAge of
// zero or less turns stale serving off.
func WithStale(maxAge time.Duration, answers ...error) Option {
	return func(s *settings) { s.stale = staleSettings{maxAge: maxAge, answers: answers} }
}

type nopObserver struct{}

func (nopObserver) Lookup(Result) {}

// state is what lookup found.
type state int

const (
	stateMiss    state = iota
	stateExpired       // reload, falling back on the expired value if it is slow
	stateFresh
	stateStale        // serve the stale value
	stateStaleRefresh // serve the stale value and retry the source
)

// Get returns the cached value for key, loading it on a miss. A caller whose
// context ends stops waiting, but the shared load carries on for the others.
func (c *Cache[V]) Get(ctx context.Context, key string) (V, error) {
	v, epoch, st := c.lookup(key)
	switch st {
	case stateFresh:
		c.observer.Lookup(Hit)
		return v, nil
	case stateStaleRefresh:
		// Retry in the background: DoChan's channel is buffered, so nobody
		// needs to read it, and the load ends within loadTimeout.
		c.flights.DoChan(flightKey(key, epoch), func() (any, error) { return c.fill(ctx, key, epoch) })
		fallthrough
	case stateStale:
		c.observer.Lookup(Stale)
		return v, nil
	}

	// DoChan runs the load on a goroutine owned by singleflight. It ends
	// when the load does, which loadTimeout bounds.
	ch := c.flights.DoChan(flightKey(key, epoch), func() (any, error) { return c.fill(ctx, key, epoch) })
	var slow <-chan time.Time
	if st == stateExpired {
		timer := time.NewTimer(staleWait)
		defer timer.Stop()
		slow = timer.C
	}
	select {
	case <-slow:
		// The load carries on and stores its result, or marks the entry
		// failing, for the lookups that follow.
		c.observer.Lookup(Stale)
		return v, nil
	case res := <-ch:
		if res.Err != nil {
			c.observer.Lookup(Miss)
			var zero V
			return zero, res.Err
		}
		f := res.Val.(filled[V])
		if f.stale {
			c.observer.Lookup(Stale)
		} else {
			c.observer.Lookup(Miss)
		}
		return f.value, nil
	case <-ctx.Done():
		c.observer.Lookup(Miss)
		var zero V
		return zero, ctx.Err()
	}
}

// flightKey shares loads only between callers that missed in the same
// epoch. A caller arriving after an invalidation therefore starts a load of
// its own instead of joining one that may have read the old value.
func flightKey(key string, epoch uint64) string {
	return key + "\x00" + strconv.FormatUint(epoch, 10)
}

// lookup returns the entry for key and what to do with it, with the epoch
// in which it was looked up.
func (c *Cache[V]) lookup(key string) (V, uint64, state) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	it, ok := c.items[key]
	if !ok {
		return zero, c.epoch, stateMiss
	}
	now := c.now()
	if now.Before(it.expires) {
		return it.value, c.epoch, stateFresh
	}
	if !now.Before(it.keep) {
		delete(c.items, key)
		return zero, c.epoch, stateMiss
	}
	if !it.failing {
		// Expired but kept: reload as usual, and fall back on it only if
		// the reload fails or is slow.
		return it.value, c.epoch, stateExpired
	}
	if now.Before(it.retryAt) {
		return it.value, c.epoch, stateStale
	}
	it.retryAt = now.Add(staleRetry)
	c.items[key] = it
	return it.value, c.epoch, stateStaleRefresh
}

// fill loads key and caches the result unless an invalidation happened
// since start, the epoch in which the callers missed. If the load fails
// because the source is unreachable and the entry is still kept, it
// returns the stale value instead.
func (c *Cache[V]) fill(ctx context.Context, key string, start uint64) (filled[V], error) {
	// A caller can miss in lookup just before another flight stores its
	// result, and reach DoChan just after that flight has ended. Checking
	// again here serves it the stored value instead of loading twice.
	c.mu.Lock()
	if it, ok := c.items[key]; ok && c.now().Before(it.expires) {
		c.mu.Unlock()
		return filled[V]{value: it.value}, nil
	}
	c.mu.Unlock()

	// The load is shared by every caller waiting on key, so the first
	// caller's cancellation must not fail it for the rest. It keeps the
	// caller's values and gets its own deadline instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
	defer cancel()
	v, err := c.load(ctx, key)

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if err != nil {
		return c.fallBack(key, now, err)
	}
	if c.epoch == start {
		c.items[key] = item[V]{value: v, expires: now.Add(c.ttl), keep: now.Add(c.ttl + max(c.stale.maxAge, 0))}
	}
	return filled[V]{value: v}, nil
}

// fallBack decides what a failed load of key returns. c.mu must be held.
//
// The entry is still in the map only if no invalidation or flush has
// dropped it, so serving it never undoes a change the cache was told about.
func (c *Cache[V]) fallBack(key string, now time.Time, err error) (filled[V], error) {
	it, ok := c.items[key]
	if !ok {
		return filled[V]{}, err
	}
	if c.isAnswer(err) {
		delete(c.items, key)
		return filled[V]{}, err
	}
	if !now.Before(it.keep) {
		return filled[V]{}, err
	}
	it.failing = true
	it.retryAt = now.Add(staleRetry)
	c.items[key] = it
	return filled[V]{value: it.value, stale: true}, nil
}

// isAnswer reports whether err is the source's answer rather than a failure
// to reach it.
func (c *Cache[V]) isAnswer(err error) bool {
	for _, a := range c.stale.answers {
		if errors.Is(err, a) {
			return true
		}
	}
	return false
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
