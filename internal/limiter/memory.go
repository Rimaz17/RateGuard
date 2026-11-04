package limiter

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Store keeps buckets and applies the engine to them atomically, so that
// concurrent requests for one key each see the state the previous one left.
// Memory satisfies it in process, and Redis across instances.
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
	buckets map[string]memoryBucket
}

type memoryBucket struct {
	state Bucket

	// fullAt is when the bucket will have refilled to capacity if nothing
	// takes from it. From then on it equals a bucket that was never used,
	// so Sweep may delete it (ADR 0011).
	fullAt time.Time
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty Memory that reads time from now, or from
// time.Now when now is nil. time.Now carries a monotonic reading, so wall
// clock adjustments cannot move a bucket's time backwards.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, buckets: make(map[string]memoryBucket)}
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
	state, d := Take(m.buckets[key].state, l, now)
	m.buckets[key] = memoryBucket{state: state, fullAt: now.Add(d.ResetAfter)}
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
	return Peek(m.buckets[key].state, l, m.now()), nil
}

func checkRequest(ctx context.Context, l Limit) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("limiter: %w", err)
	}
	return l.Validate()
}

// Sweep deletes every bucket that has refilled to capacity, and returns how
// many it deleted. Such a bucket decides exactly as a missing one does, so
// sweeping changes no decision; it only stops keys that are never seen
// again, such as passing IP addresses, from accumulating (ADR 0011).
func (m *Memory) Sweep() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	deleted := 0
	for key, b := range m.buckets {
		if !now.Before(b.fullAt) {
			delete(m.buckets, key)
			deleted++
		}
	}
	return deleted
}

// Run sweeps the store every interval until ctx is done. The caller owns the
// goroutine it runs on.
func (m *Memory) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}

// Len returns how many buckets the store holds.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.buckets)
}
