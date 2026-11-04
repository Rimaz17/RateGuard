package limiter_test

import (
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// TestRefillBoundaries empties a bucket and checks that the next request is
// refused a nanosecond before its token is due and allowed exactly when it
// is, for rates whose token interval is exact in floating point and for
// rates where it is not.
func TestRefillBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		limit limiter.Limit
		due   time.Duration // one token's refill time
	}{
		{"2/s, exact", limiter.Limit{Capacity: 1, RefillRate: 2}, 500 * time.Millisecond},
		{"10/s, inexact", limiter.Limit{Capacity: 1, RefillRate: 10}, 100 * time.Millisecond},
		{"100/min, inexact", limiter.Limit{Capacity: 100, RefillRate: 100.0 / 60}, 600 * time.Millisecond},
		{"5/min", limiter.Limit{Capacity: 5, RefillRate: 5.0 / 60}, 12 * time.Second},
		{"1000/min, inexact", limiter.Limit{Capacity: 1000, RefillRate: 1000.0 / 60}, 60 * time.Millisecond},
		{"5000/min, inexact", limiter.Limit{Capacity: 5000, RefillRate: 5000.0 / 60}, 12 * time.Millisecond},
		{"3/s, a third of a second", limiter.Limit{Capacity: 1, RefillRate: 3}, 333333334 * time.Nanosecond},
		{"1/day", limiter.Limit{Capacity: 1, RefillRate: 1.0 / 86400}, 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := takeN(t, limiter.Bucket{}, tt.limit, t0, tt.limit.Capacity)

			_, d := limiter.Take(b, tt.limit, t0)
			if d.Allowed {
				t.Fatal("a take from the emptied bucket was allowed")
			}
			if diff := d.RetryAfter - tt.due; diff < 0 || diff > 2*time.Nanosecond {
				t.Errorf("RetryAfter = %v, want %v (rounded up by at most 2ns)", d.RetryAfter, tt.due)
			}

			// The billionth-of-a-token tolerance is 1e-9/rate seconds early
			// (86µs at one a day); probe just outside it, and no closer than
			// a microsecond.
			early := max(time.Microsecond, time.Duration(2e-9/tt.limit.RefillRate*float64(time.Second)))
			if _, d := limiter.Take(b, tt.limit, t0.Add(tt.due-early)); d.Allowed {
				t.Errorf("allowed %v before the token was due", early)
			}
			if _, onTime := limiter.Take(b, tt.limit, t0.Add(d.RetryAfter)); !onTime.Allowed {
				t.Errorf("refused at exactly RetryAfter (%v): %+v", d.RetryAfter, onTime)
			}
			if _, onTime := limiter.Take(b, tt.limit, t0.Add(tt.due)); !onTime.Allowed && tt.due >= d.RetryAfter {
				t.Errorf("refused at exactly the token's refill time %v", tt.due)
			}
		})
	}
}

// TestRefillDoesNotDependOnHowOftenTheBucketIsSeen drives one bucket with
// a request every millisecond, most of them refused, and another with
// requests only at the moments the first admitted one. Lazy refill must
// not care how often it is computed: both admit at the same moments and
// end in the same state.
func TestRefillDoesNotDependOnHowOftenTheBucketIsSeen(t *testing.T) {
	l := limiter.Limit{Capacity: 100, RefillRate: 100.0 / 60}
	busy, _ := takeN(t, limiter.Bucket{}, l, t0, 100)
	sparse := busy

	var admitted []time.Time
	for ms := 1; ms <= 30_000; ms++ {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		var d limiter.Decision
		if busy, d = limiter.Take(busy, l, now); d.Allowed {
			admitted = append(admitted, now)
		}
	}
	if len(admitted) != 50 {
		t.Fatalf("30s at 100/min admitted %d, want 50", len(admitted))
	}
	for i, at := range admitted {
		var d limiter.Decision
		if sparse, d = limiter.Take(sparse, l, at); !d.Allowed {
			t.Fatalf("the sparse bucket refused admission %d at %v", i+1, at.Sub(t0))
		}
	}

	end := t0.Add(30 * time.Second)
	if db, ds := limiter.Peek(busy, l, end), limiter.Peek(sparse, l, end); db != ds {
		t.Errorf("at the end the busy bucket says %+v and the sparse one %+v", db, ds)
	}
}

func TestZeroElapsedRefillsNothing(t *testing.T) {
	l := limiter.Limit{Capacity: 2, RefillRate: 1000}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 2)
	for range 100 {
		var d limiter.Decision
		if b, d = limiter.Take(b, l, t0); d.Allowed {
			t.Fatal("a take at the same instant as the last refill was allowed")
		}
	}
	if b.Tokens != 0 || !b.LastRefill.Equal(t0) {
		t.Errorf("bucket = %+v, want empty as of t0", b)
	}
}

func TestBackwardsClockRefillsNothingAndKeepsLastRefill(t *testing.T) {
	l := limiter.Limit{Capacity: 4, RefillRate: 2} // a token every 500ms
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 4)

	// The clock jumps back a second: nothing refills, and the last refill
	// time stays at t0 rather than moving back with it.
	back, d := limiter.Take(b, l, t0.Add(-time.Second))
	if d.Allowed || back.Tokens != 0 || !back.LastRefill.Equal(t0) {
		t.Fatalf("after the clock went back: %+v, %+v; want refused, empty, last refill still t0", back, d)
	}

	// When the clock catches up, only time after t0 counts. Had the
	// backwards reading been stored, 1.5s would have refilled 3 tokens.
	d = limiter.Peek(back, l, t0.Add(500*time.Millisecond))
	if d.Remaining != 1 {
		t.Errorf("500ms after t0 = %+v, want exactly 1 token", d)
	}
}

func TestBackwardsClockKeepsTheBalance(t *testing.T) {
	l := limiter.Limit{Capacity: 10, RefillRate: 1}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 3)

	b, d := limiter.Take(b, l, t0.Add(-time.Hour))
	if !d.Allowed || d.Remaining != 6 || !b.LastRefill.Equal(t0) {
		t.Errorf("a take with the clock an hour behind = %+v, %+v; want allowed from the existing 7 tokens, last refill kept", d, b)
	}
}

func TestLongIdleDoesNotOverflow(t *testing.T) {
	l := limiter.Limit{Capacity: 5, RefillRate: 5000.0 / 60}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 5)

	// A bucket last touched centuries ago is simply full.
	b.LastRefill = time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC)
	_, d := limiter.Take(b, l, t0)
	if !d.Allowed || d.Remaining != 4 {
		t.Errorf("after a very long idle = %+v, want full then one taken", d)
	}
}
