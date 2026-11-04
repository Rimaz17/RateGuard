package limiter_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// t0 is an arbitrary instant; the engine only ever looks at differences.
var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// perMinute is the overview's Free plan: 100 requests a minute.
var perMinute = limiter.Limit{Capacity: 100, RefillRate: 100.0 / 60}

// takeN takes n tokens at the same instant and returns the bucket and the
// last decision.
func takeN(t *testing.T, b limiter.Bucket, l limiter.Limit, now time.Time, n int) (limiter.Bucket, limiter.Decision) {
	t.Helper()
	var d limiter.Decision
	for i := range n {
		b, d = limiter.Take(b, l, now)
		if !d.Allowed {
			t.Fatalf("take %d of %d was refused: %+v", i+1, n, d)
		}
	}
	return b, d
}

func TestNewBucketStartsFull(t *testing.T) {
	b, d := limiter.Take(limiter.Bucket{}, perMinute, t0)
	want := limiter.Decision{Allowed: true, Limit: 100, Remaining: 99, ResetAfter: 600 * time.Millisecond}
	if d != want {
		t.Errorf("first take = %+v, want %+v", d, want)
	}
	if b.Tokens != 99 || !b.LastRefill.Equal(t0) {
		t.Errorf("bucket after the first take = %+v, want 99 tokens as of t0", b)
	}
}

func TestDecisionFields(t *testing.T) {
	l := limiter.Limit{Capacity: 10, RefillRate: 2}

	b, d := takeN(t, limiter.Bucket{}, l, t0, 4)
	want := limiter.Decision{Allowed: true, Limit: 10, Remaining: 6, ResetAfter: 2 * time.Second}
	if d != want {
		t.Errorf("after 4 takes = %+v, want %+v", d, want)
	}

	b, d = takeN(t, b, l, t0, 6)
	want = limiter.Decision{Allowed: true, Limit: 10, Remaining: 0, ResetAfter: 5 * time.Second}
	if d != want {
		t.Errorf("after 10 takes = %+v, want %+v", d, want)
	}

	_, d = limiter.Take(b, l, t0)
	want = limiter.Decision{Allowed: false, Limit: 10, Remaining: 0, ResetAfter: 5 * time.Second, RetryAfter: 500 * time.Millisecond}
	if d != want {
		t.Errorf("the 11th take = %+v, want %+v", d, want)
	}
}

func TestRejectionLeavesTheBucketAsItWas(t *testing.T) {
	l := limiter.Limit{Capacity: 1, RefillRate: 1}
	b, _ := limiter.Take(limiter.Bucket{}, l, t0)
	now := t0.Add(300 * time.Millisecond)

	after, d := limiter.Take(b, l, now)
	if d.Allowed {
		t.Fatal("a take from an empty bucket was allowed")
	}
	if math.Abs(after.Tokens-0.3) > 1e-12 || !after.LastRefill.Equal(now) {
		t.Errorf("bucket after a rejection = %+v, want 0.3 tokens refilled to now and nothing taken", after)
	}
}

func TestPeekDoesNotConsume(t *testing.T) {
	l := limiter.Limit{Capacity: 3, RefillRate: 1}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 1)

	for range 5 {
		d := limiter.Peek(b, l, t0)
		want := limiter.Decision{Allowed: true, Limit: 3, Remaining: 2, ResetAfter: time.Second}
		if d != want {
			t.Fatalf("Peek = %+v, want %+v", d, want)
		}
	}
	if fresh := limiter.Peek(limiter.Bucket{}, l, t0); fresh != (limiter.Decision{Allowed: true, Limit: 3, Remaining: 3}) {
		t.Errorf("Peek of a new bucket = %+v, want full with nothing to reset", fresh)
	}

	b, _ = takeN(t, b, l, t0, 2)
	d := limiter.Peek(b, l, t0)
	want := limiter.Decision{Allowed: false, Limit: 3, Remaining: 0, ResetAfter: 3 * time.Second, RetryAfter: time.Second}
	if d != want {
		t.Errorf("Peek of an empty bucket = %+v, want %+v", d, want)
	}
}

func TestLimitValidate(t *testing.T) {
	valid := []limiter.Limit{
		{Capacity: 1, RefillRate: 1e-9},
		{Capacity: 100, RefillRate: 100.0 / 60},
		{Capacity: math.MaxInt32, RefillRate: 1e9},
	}
	for _, l := range valid {
		if err := l.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", l, err)
		}
	}
	invalid := []limiter.Limit{
		{Capacity: 0, RefillRate: 1},
		{Capacity: -5, RefillRate: 1},
		{Capacity: 10, RefillRate: 0},
		{Capacity: 10, RefillRate: -1},
		{Capacity: 10, RefillRate: math.NaN()},
		{Capacity: 10, RefillRate: math.Inf(1)},
		{Capacity: 10, RefillRate: math.Inf(-1)},
	}
	for _, l := range invalid {
		if err := l.Validate(); !errors.Is(err, limiter.ErrInvalidLimit) {
			t.Errorf("Validate(%+v) = %v, want ErrInvalidLimit", l, err)
		}
	}
}
