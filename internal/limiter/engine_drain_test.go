package limiter_test

import (
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

func TestBurstUpToCapacityThenRefuse(t *testing.T) {
	for _, l := range []limiter.Limit{
		{Capacity: 1, RefillRate: 1},
		{Capacity: 5, RefillRate: 5.0 / 60},
		perMinute,
		{Capacity: 1000, RefillRate: 1000.0 / 60},
		{Capacity: 5000, RefillRate: 5000.0 / 60},
	} {
		b := limiter.Bucket{}
		var d limiter.Decision
		for i := range l.Capacity {
			b, d = limiter.Take(b, l, t0)
			if !d.Allowed || d.Remaining != l.Capacity-1-i {
				t.Fatalf("%+v: take %d = %+v, want allowed with %d remaining", l, i+1, d, l.Capacity-1-i)
			}
		}
		for range 3 {
			b, d = limiter.Take(b, l, t0)
			if d.Allowed || d.Remaining != 0 {
				t.Fatalf("%+v: a take past the capacity = %+v, want refused", l, d)
			}
		}
		wantRetry := time.Duration(float64(time.Second) / l.RefillRate)
		if diff := d.RetryAfter - wantRetry; diff < 0 || diff > time.Nanosecond {
			t.Errorf("%+v: RetryAfter = %v, want one token's refill time %v (rounded up)", l, d.RetryAfter, wantRetry)
		}
	}
}

// TestSteadyDrainHoldsTheLongRunRate sends requests far faster than the
// refill rate for a long time. Exactly the initial burst plus what refilled
// may get through, however the requests are spaced.
func TestSteadyDrainHoldsTheLongRunRate(t *testing.T) {
	tests := []struct {
		name     string
		limit    limiter.Limit
		interval time.Duration
		duration time.Duration
	}{
		{"10/s capacity 10, a request every 10ms for 10s", limiter.Limit{Capacity: 10, RefillRate: 10}, 10 * time.Millisecond, 10 * time.Second},
		{"Free plan, a request every 7ms for 5 minutes", perMinute, 7 * time.Millisecond, 5 * time.Minute},
		{"login override 5/min, a request every 250ms for an hour", limiter.Limit{Capacity: 5, RefillRate: 5.0 / 60}, 250 * time.Millisecond, time.Hour},
		{"bursty: capacity 50 at 1/s, a request every 3ms for 2 minutes", limiter.Limit{Capacity: 50, RefillRate: 1}, 3 * time.Millisecond, 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := limiter.Bucket{}
			allowed := 0
			var elapsed time.Duration
			for elapsed = 0; elapsed <= tt.duration; elapsed += tt.interval {
				var d limiter.Decision
				b, d = limiter.Take(b, tt.limit, t0.Add(elapsed))
				if d.Allowed {
					allowed++
				}
			}
			last := elapsed - tt.interval
			supply := float64(tt.limit.Capacity) + last.Seconds()*tt.limit.RefillRate
			// Demand outstrips supply, so every whole token is used: the
			// count is the supply rounded down, never more.
			if want := int(supply + 1e-9); allowed != want {
				t.Errorf("allowed %d requests, want exactly %d (capacity %d plus %.3f refilled over %v)",
					allowed, want, tt.limit.Capacity, last.Seconds()*tt.limit.RefillRate, last)
			}
		})
	}
}

// TestSteadyDrainSpacesRequestsByTheRefillTime checks that once the burst
// is spent, admissions come exactly one refill interval apart.
func TestSteadyDrainSpacesRequestsByTheRefillTime(t *testing.T) {
	l := limiter.Limit{Capacity: 3, RefillRate: 4} // a token every 250ms
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 3)

	var admitted []time.Duration
	for ms := 1; ms <= 2000; ms++ {
		var d limiter.Decision
		b, d = limiter.Take(b, l, t0.Add(time.Duration(ms)*time.Millisecond))
		if d.Allowed {
			admitted = append(admitted, time.Duration(ms)*time.Millisecond)
		}
	}
	if len(admitted) != 8 {
		t.Fatalf("admitted %d requests in 2s after the burst, want 8: %v", len(admitted), admitted)
	}
	for i, at := range admitted {
		if want := time.Duration(i+1) * 250 * time.Millisecond; at != want {
			t.Errorf("admission %d at %v, want %v", i+1, at, want)
		}
	}
}

func TestIdleBucketRefillsToCapacity(t *testing.T) {
	l := limiter.Limit{Capacity: 10, RefillRate: 2}
	b, _ := takeN(t, limiter.Bucket{}, l, t0, 10)

	d := limiter.Peek(b, l, t0.Add(2*time.Second))
	if d.Remaining != 4 || d.ResetAfter != 3*time.Second {
		t.Errorf("after 2s idle = %+v, want 4 tokens and 3s to full", d)
	}
	d = limiter.Peek(b, l, t0.Add(5*time.Second))
	if d.Remaining != 10 || d.ResetAfter != 0 {
		t.Errorf("after 5s idle = %+v, want full", d)
	}
	b, _ = takeN(t, b, l, t0.Add(5*time.Second), 10)
	if _, d := limiter.Take(b, l, t0.Add(5*time.Second)); d.Allowed {
		t.Error("a second full burst got more than the capacity")
	}
}
