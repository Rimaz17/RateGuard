package limiter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

func TestSweepDeletesOnlyFullBuckets(t *testing.T) {
	c := newClock()
	m := limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 10, RefillRate: 1} // 1 token takes 1s to return

	mustTake(t, m, "one-taken", l) // full again 1s later
	for range 5 {
		mustTake(t, m, "five-taken", l) // full again 5s later
	}

	if n := m.Sweep(); n != 0 || m.Len() != 2 {
		t.Fatalf("sweep at once deleted %d, leaving %d; want nothing deleted", n, m.Len())
	}
	c.Advance(time.Second - time.Nanosecond)
	if n := m.Sweep(); n != 0 {
		t.Fatalf("sweep a nanosecond early deleted %d", n)
	}
	c.Advance(time.Nanosecond)
	if n := m.Sweep(); n != 1 || m.Len() != 1 {
		t.Fatalf("sweep at 1s deleted %d, leaving %d; want the full bucket gone", n, m.Len())
	}
	c.Advance(4 * time.Second)
	if n := m.Sweep(); n != 1 || m.Len() != 0 {
		t.Fatalf("sweep at 5s deleted %d, leaving %d; want everything gone", n, m.Len())
	}
}

// TestSweepChangesNoDecision runs the same traffic through two stores, one
// swept constantly, and expects identical decisions throughout.
func TestSweepChangesNoDecision(t *testing.T) {
	c := newClock()
	swept, kept := limiter.NewMemory(c.Now), limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 3, RefillRate: 2}

	deleted := 0
	for step := range 2000 {
		key := fmt.Sprintf("k%d", step%7)
		// Bursts, then gaps long enough for some buckets to fill again.
		if step%50 == 0 {
			c.Advance(3 * time.Second)
		} else {
			c.Advance(time.Duration(step%13) * 10 * time.Millisecond)
		}
		ds, dk := mustTake(t, swept, key, l), mustTake(t, kept, key, l)
		if ds != dk {
			t.Fatalf("step %d, %s: swept store decided %+v, unswept %+v", step, key, ds, dk)
		}
		deleted += swept.Sweep()
	}
	if deleted == 0 {
		t.Fatal("nothing was ever swept, so the comparison proves nothing")
	}
}

func TestSweptBucketGetsNoExtraTokens(t *testing.T) {
	c := newClock()
	m := limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 2, RefillRate: 1}

	mustTake(t, m, "k", l)
	mustTake(t, m, "k", l)
	c.Advance(2 * time.Second)
	if m.Sweep() != 1 {
		t.Fatal("the refilled bucket was not swept")
	}
	allowed := 0
	for range 5 {
		if mustTake(t, m, "k", l).Allowed {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("a swept bucket admitted %d at once, want its capacity of 2", allowed)
	}
}

func TestRunSweepsUntilCancelled(t *testing.T) {
	c := newClock()
	m := limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 1, RefillRate: 1000}
	for i := range 100 {
		mustTake(t, m, fmt.Sprintf("ip-%d", i), l)
	}
	c.Advance(time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Run(ctx, time.Millisecond)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for m.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("Run left %d buckets after 5s", m.Len())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
