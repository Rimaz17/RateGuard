package limiter_test

import (
	"math"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// FuzzEngineInvariants runs random request sequences, with time moving
// forwards, standing still and jumping back, against random limits, and
// checks what ADR 0011 promises after every step:
//
//   - a decision is within bounds: 0 <= remaining <= limit, and a refusal
//     has nothing remaining and a positive retry_after;
//   - Peek predicts Take exactly;
//   - retrying after retry_after, with nothing else using the bucket, is
//     allowed;
//   - after reset_after the bucket is full;
//   - allowed requests never exceed capacity + rate x elapsed.
func FuzzEngineInvariants(f *testing.F) {
	f.Add(uint16(100), uint32(1667), []byte{0, 0, 0, 10, 200, 3, 255, 7})
	f.Add(uint16(5), uint32(83), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add(uint16(1), uint32(1), []byte{1, 2, 3, 250, 251, 4})
	f.Add(uint16(999), uint32(999999), []byte{9, 9, 9, 9, 9, 9})
	f.Fuzz(func(t *testing.T, capacity uint16, rateMilli uint32, steps []byte) {
		l := limiter.Limit{
			Capacity:   int(capacity%1000) + 1,
			RefillRate: float64(rateMilli%1_000_000+1) / 1000, // 0.001 to 1000 per second
		}
		var (
			b       limiter.Bucket
			now     = t0
			first   time.Time // when the bucket was created, by its first take
			latest  time.Time // the latest time it has seen
			allowed int
		)
		for _, step := range steps {
			switch {
			case step >= 250:
				now = now.Add(-time.Duration(step-249) * time.Second) // the clock jumps back
			default:
				now = now.Add(time.Duration(step%64) * time.Duration(step/64+1) * time.Millisecond)
			}
			if first.IsZero() {
				first, latest = now, now
			}
			if now.After(latest) {
				latest = now
			}

			peek := limiter.Peek(b, l, now)
			var d limiter.Decision
			b, d = limiter.Take(b, l, now)

			checkBounds(t, l, d)
			if peek.Allowed != d.Allowed {
				t.Fatalf("Peek said allowed=%v but Take said %v at the same instant", peek.Allowed, d.Allowed)
			}
			if b.Tokens < 0 || b.Tokens > float64(l.Capacity) {
				t.Fatalf("bucket holds %v tokens, outside [0, %d]", b.Tokens, l.Capacity)
			}
			if d.Allowed {
				allowed++
			} else if d.RetryAfter < math.MaxInt64 {
				if _, retry := limiter.Take(b, l, now.Add(d.RetryAfter)); !retry.Allowed {
					t.Fatalf("refused again after waiting RetryAfter %v: %+v", d.RetryAfter, retry)
				}
			}
			if d.ResetAfter < math.MaxInt64 {
				if full := limiter.Peek(b, l, now.Add(d.ResetAfter)); full.Remaining != l.Capacity {
					t.Fatalf("after ResetAfter %v the bucket holds %d, want %d", d.ResetAfter, full.Remaining, l.Capacity)
				}
			}

			supply := float64(l.Capacity) + latest.Sub(first).Seconds()*l.RefillRate
			if float64(allowed) > supply+1e-6 {
				t.Fatalf("allowed %d requests, more than the %.6f the limit supplies by %v", allowed, supply, latest.Sub(first))
			}
		}
	})
}

func checkBounds(t *testing.T, l limiter.Limit, d limiter.Decision) {
	t.Helper()
	if d.Limit != l.Capacity || d.Remaining < 0 || d.Remaining > l.Capacity || d.ResetAfter < 0 {
		t.Fatalf("decision %+v is out of bounds for %+v", d, l)
	}
	if d.Allowed && d.RetryAfter != 0 {
		t.Fatalf("an allowed decision has RetryAfter %v", d.RetryAfter)
	}
	if !d.Allowed && (d.Remaining != 0 || d.RetryAfter <= 0) {
		t.Fatalf("a refusal %+v has tokens left or no RetryAfter", d)
	}
}
