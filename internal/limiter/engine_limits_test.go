package limiter_test

import (
	"math"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

func TestTokensNeverExceedTheCapacity(t *testing.T) {
	l := limiter.Limit{Capacity: 10, RefillRate: 1000}
	b, _ := limiter.Take(limiter.Bucket{}, l, t0)

	for _, idle := range []time.Duration{time.Second, time.Hour, 365 * 24 * time.Hour} {
		d := limiter.Peek(b, l, t0.Add(idle))
		if d.Remaining != 10 || d.ResetAfter != 0 {
			t.Errorf("after %v idle = %+v, want exactly the capacity and nothing to reset", idle, d)
		}
	}
	b, _ = limiter.Take(b, l, t0.Add(time.Hour))
	if b.Tokens != 9 {
		t.Errorf("tokens after an hour idle and one take = %v, want 9", b.Tokens)
	}
}

// TestLoweredCapacityClampsAtOnce models an admin cutting a plan from 100 to
// 10 while a client holds 80 unused tokens: the client gets 10, not 80.
func TestLoweredCapacityClampsAtOnce(t *testing.T) {
	before := limiter.Limit{Capacity: 100, RefillRate: 100.0 / 60}
	after := limiter.Limit{Capacity: 10, RefillRate: 10.0 / 60}
	b, _ := takeN(t, limiter.Bucket{}, before, t0, 20)

	b, d := takeN(t, b, after, t0, 10)
	if d.Remaining != 0 || d.Limit != 10 {
		t.Errorf("after 10 takes on the lowered limit = %+v, want empty at limit 10", d)
	}
	if _, d = limiter.Take(b, after, t0); d.Allowed {
		t.Error("the 11th take on a capacity of 10 was allowed")
	}

	// A stored balance above the capacity is clamped even without a take.
	stale := limiter.Bucket{Tokens: 80, LastRefill: t0}
	if d := limiter.Peek(stale, after, t0); d.Remaining != 10 {
		t.Errorf("Peek of 80 stored tokens at capacity 10 = %+v, want 10", d)
	}
}

// TestRaisedCapacityRefillsGradually models an upgrade: the client keeps
// its balance and refills towards the new capacity at the new rate.
func TestRaisedCapacityRefillsGradually(t *testing.T) {
	free := limiter.Limit{Capacity: 10, RefillRate: 1}
	premium := limiter.Limit{Capacity: 100, RefillRate: 10}
	b, _ := takeN(t, limiter.Bucket{}, free, t0, 10)

	d := limiter.Peek(b, premium, t0)
	if d.Remaining != 0 || d.Limit != 100 || d.ResetAfter != 10*time.Second {
		t.Errorf("right after the upgrade = %+v, want empty, limit 100, 10s to full", d)
	}
	d = limiter.Peek(b, premium, t0.Add(2*time.Second))
	if d.Remaining != 20 {
		t.Errorf("2s after the upgrade = %+v, want 20 tokens at the new rate", d)
	}
}

func TestDegenerateLimitsNeverPanicOrAllowUnlimited(t *testing.T) {
	tests := []struct {
		name  string
		limit limiter.Limit
	}{
		{"zero capacity", limiter.Limit{Capacity: 0, RefillRate: 10}},
		{"negative capacity", limiter.Limit{Capacity: -3, RefillRate: 10}},
		{"zero rate", limiter.Limit{Capacity: 2, RefillRate: 0}},
		{"negative rate", limiter.Limit{Capacity: 2, RefillRate: -1}},
		{"NaN rate", limiter.Limit{Capacity: 2, RefillRate: math.NaN()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := limiter.Bucket{}
			allowed := 0
			for s := range 1000 {
				var d limiter.Decision
				b, d = limiter.Take(b, tt.limit, t0.Add(time.Duration(s)*time.Second))
				if d.Allowed {
					allowed++
				}
				if d.Remaining < 0 || d.Remaining > max(tt.limit.Capacity, 0) || d.Limit != max(tt.limit.Capacity, 0) {
					t.Fatalf("decision %+v is out of range", d)
				}
				if !d.Allowed && d.RetryAfter <= 0 {
					t.Fatalf("a refusal with RetryAfter %v", d.RetryAfter)
				}
			}
			// With no capacity nothing gets through; with no refill, only
			// the first burst does.
			if want := max(tt.limit.Capacity, 0); allowed != want {
				t.Errorf("allowed %d over 1000s, want %d", allowed, want)
			}
		})
	}
}

func TestInfiniteRateRefillsInstantly(t *testing.T) {
	l := limiter.Limit{Capacity: 2, RefillRate: math.Inf(1)}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 2)
	if _, d := limiter.Take(b, l, t0); d.Allowed {
		t.Error("allowed with no time elapsed")
	}
	if _, d := limiter.Take(b, l, t0.Add(time.Nanosecond)); !d.Allowed || d.Remaining != 1 || d.ResetAfter != 0 {
		t.Errorf("a nanosecond later = %+v, want full again", d)
	}
}

func TestDurationsSaturateInsteadOfOverflowing(t *testing.T) {
	for _, rate := range []float64{1e-300, 1e-20, 1e-12} {
		l := limiter.Limit{Capacity: 1, RefillRate: rate}
		b, _ := limiter.Take(limiter.Bucket{}, l, t0)
		_, d := limiter.Take(b, l, t0)
		if d.Allowed || d.RetryAfter <= 0 || d.ResetAfter <= 0 {
			t.Errorf("rate %g: %+v, want a refusal with positive durations", rate, d)
		}
		if rate < 1e-15 && (d.RetryAfter != math.MaxInt64 || d.ResetAfter != math.MaxInt64) {
			t.Errorf("rate %g: RetryAfter %v ResetAfter %v, want both saturated", rate, d.RetryAfter, d.ResetAfter)
		}
	}
}

// TestSaturatedDurationsSurviveABackwardsClock adds the lag of a clock
// that went back to durations that are already at the maximum: the sum
// must stay at the maximum, not wrap negative.
func TestSaturatedDurationsSurviveABackwardsClock(t *testing.T) {
	l := limiter.Limit{Capacity: 1, RefillRate: 1e-300}
	b, _ := limiter.Take(limiter.Bucket{}, l, t0)

	_, d := limiter.Take(b, l, t0.Add(-time.Hour))
	if d.Allowed || d.RetryAfter != math.MaxInt64 || d.ResetAfter != math.MaxInt64 {
		t.Errorf("an hour behind at a negligible rate = %+v, want both durations saturated", d)
	}
}

func TestCorruptStoredStateIsTreatedAsEmpty(t *testing.T) {
	l := limiter.Limit{Capacity: 5, RefillRate: 1}
	for _, tokens := range []float64{math.NaN(), -1, math.Inf(-1)} {
		b := limiter.Bucket{Tokens: tokens, LastRefill: t0}
		if _, d := limiter.Take(b, l, t0); d.Allowed || d.Remaining != 0 {
			t.Errorf("stored tokens %v: %+v, want treated as empty", tokens, d)
		}
		if d := limiter.Peek(b, l, t0.Add(2*time.Second)); d.Remaining != 2 {
			t.Errorf("stored tokens %v, 2s later: %+v, want 2 refilled from empty", tokens, d)
		}
	}
	// Infinite stored tokens are clamped to the capacity.
	b := limiter.Bucket{Tokens: math.Inf(1), LastRefill: t0}
	if d := limiter.Peek(b, l, t0); d.Remaining != 5 {
		t.Errorf("stored +Inf tokens: %+v, want clamped to 5", d)
	}
}
