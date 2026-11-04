package limiter_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// clock is a settable time source for the store.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: t0} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func mustTake(t *testing.T, s limiter.Store, key string, l limiter.Limit) limiter.Decision {
	t.Helper()
	d, err := s.Take(t.Context(), key, l)
	if err != nil {
		t.Fatalf("Take(%q): %v", key, err)
	}
	return d
}

func TestMemoryAppliesTheEngine(t *testing.T) {
	c := newClock()
	m := limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 2, RefillRate: 1}

	got := []limiter.Decision{mustTake(t, m, "k", l), mustTake(t, m, "k", l), mustTake(t, m, "k", l)}
	want := []limiter.Decision{
		{Allowed: true, Limit: 2, Remaining: 1, ResetAfter: time.Second},
		{Allowed: true, Limit: 2, Remaining: 0, ResetAfter: 2 * time.Second},
		{Allowed: false, Limit: 2, Remaining: 0, ResetAfter: 2 * time.Second, RetryAfter: time.Second},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("take %d = %+v, want %+v", i+1, got[i], want[i])
		}
	}

	c.Advance(time.Second)
	if d := mustTake(t, m, "k", l); !d.Allowed {
		t.Errorf("a second later = %+v, want allowed", d)
	}
}

func TestMemoryKeysAreIndependent(t *testing.T) {
	m := limiter.NewMemory(newClock().Now)
	l := limiter.Limit{Capacity: 1, RefillRate: 1}

	if d := mustTake(t, m, "client-a", l); !d.Allowed {
		t.Fatal("first take for client-a refused")
	}
	if d := mustTake(t, m, "client-a", l); d.Allowed {
		t.Fatal("second take for client-a allowed")
	}
	for _, key := range []string{"client-b", "client-a:/api/login", "", "CLIENT-A"} {
		if d := mustTake(t, m, key, l); !d.Allowed {
			t.Errorf("key %q shared client-a's bucket", key)
		}
	}
	if n := m.Len(); n != 5 {
		t.Errorf("Len = %d, want 5 buckets", n)
	}
}

func TestMemoryPeekNeitherConsumesNorStores(t *testing.T) {
	m := limiter.NewMemory(newClock().Now)
	l := limiter.Limit{Capacity: 3, RefillRate: 1}

	for range 10 {
		d, err := m.Peek(t.Context(), "never-taken", l)
		if err != nil || d != (limiter.Decision{Allowed: true, Limit: 3, Remaining: 3}) {
			t.Fatalf("Peek of an unused key = %+v, %v; want full", d, err)
		}
	}
	if n := m.Len(); n != 0 {
		t.Errorf("Len after peeks = %d, want 0", n)
	}

	mustTake(t, m, "k", l)
	for range 3 {
		if d, _ := m.Peek(t.Context(), "k", l); d.Remaining != 2 {
			t.Fatalf("Peek after one take = %+v, want 2 remaining every time", d)
		}
	}
}

func TestMemoryRefusesInvalidLimits(t *testing.T) {
	m := limiter.NewMemory(nil)
	for _, l := range []limiter.Limit{
		{Capacity: 0, RefillRate: 1},
		{Capacity: 1, RefillRate: 0},
		{Capacity: 1, RefillRate: math.NaN()},
		{Capacity: 1, RefillRate: math.Inf(1)},
	} {
		if _, err := m.Take(t.Context(), "k", l); !errors.Is(err, limiter.ErrInvalidLimit) {
			t.Errorf("Take with %+v: error = %v, want ErrInvalidLimit", l, err)
		}
		if _, err := m.Peek(t.Context(), "k", l); !errors.Is(err, limiter.ErrInvalidLimit) {
			t.Errorf("Peek with %+v: error = %v, want ErrInvalidLimit", l, err)
		}
	}
	if n := m.Len(); n != 0 {
		t.Errorf("Len = %d, want refused requests to create nothing", n)
	}
}

func TestMemoryHonoursCancellation(t *testing.T) {
	m := limiter.NewMemory(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := limiter.Limit{Capacity: 1, RefillRate: 1}

	if _, err := m.Take(ctx, "k", l); !errors.Is(err, context.Canceled) {
		t.Errorf("Take with a cancelled context: error = %v, want context.Canceled", err)
	}
	if _, err := m.Peek(ctx, "k", l); !errors.Is(err, context.Canceled) {
		t.Errorf("Peek with a cancelled context: error = %v, want context.Canceled", err)
	}
	if d := mustTake(t, m, "k", l); !d.Allowed {
		t.Error("a cancelled Take consumed the bucket's token")
	}
}

func TestMemoryDefaultClockIsReal(t *testing.T) {
	m := limiter.NewMemory(nil)
	l := limiter.Limit{Capacity: 1, RefillRate: 50} // a token every 20ms
	mustTake(t, m, "k", l)
	if d := mustTake(t, m, "k", l); d.Allowed {
		t.Fatal("an immediate second take was allowed")
	}
	time.Sleep(60 * time.Millisecond)
	if d := mustTake(t, m, "k", l); !d.Allowed {
		t.Errorf("60ms later at 50/s = %+v, want allowed", d)
	}
}
