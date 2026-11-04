package limiter

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Store keeps buckets and applies the engine to them atomically, so that
// concurrent requests for one key each see the state the previous one left.
// Memory satisfies it in process; the Redis store of phase 5 will satisfy it
// across instances.
type Store interface {
	// Take takes one token from the bucket at key, refilling it first.
	Take(ctx context.Context, key string, l Limit) (Decision, error)

	// Peek reports what Take would decide, without taking a token.
	Peek(ctx context.Context, key string, l Limit) (Decision, error)
}

// Memory is a Store that keeps buckets in this process. It is exact for one
// instance, and it is the bucket RateGuard falls back to when Redis is
// unreachable (ADR 0001).
//
// Its clock is the process's own: bucket state never leaves the process,
// so there is no other clock for it to disagree with (ADR 0002). A Memory is
// safe for concurrent use.
type Memory struct {
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]Bucket
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty Memory that reads time from now, or from
// time.Now when now is nil. time.Now carries a monotonic reading, so wall
// clock adjustments cannot move a bucket's time backwards.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, buckets: make(map[string]Bucket)}
}

// Take implements Store. An invalid limit is refused with ErrInvalidLimit.
func (m *Memory) Take(ctx context.Context, key string, l Limit) (Decision, error) {
	if err := checkRequest(ctx, l); err != nil {
		return Decision{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// The clock is read under the lock so that the order in which requests
	// change a bucket is also the order of their times.
	now := m.now()
	state, d := Take(m.buckets[key], l, now)
	m.buckets[key] = state
	return d, nil
}

// Peek implements Store. It stores nothing, so peeking at a key never makes
// the store grow.
func (m *Memory) Peek(ctx context.Context, key string, l Limit) (Decision, error) {
	if err := checkRequest(ctx, l); err != nil {
		return Decision{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return Peek(m.buckets[key], l, m.now()), nil
}

func checkRequest(ctx context.Context, l Limit) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("limiter: %w", err)
	}
	return l.Validate()
}

// Len returns how many buckets the store holds.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.buckets)
}
