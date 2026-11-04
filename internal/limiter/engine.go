package limiter

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// wholeTokenTolerance is how far below a whole token a balance may fall and
// still count as whole. Refill arithmetic in floating point can land a hair
// short at the very moment a token should be complete; ADR 0011 explains
// why a billionth of a token is the right leniency.
const wholeTokenTolerance = 1e-9

// ErrInvalidLimit means a limit cannot describe a bucket: its capacity is
// below one or its refill rate is not a positive, finite number.
var ErrInvalidLimit = errors.New("invalid limit")

// Limit configures a bucket.
type Limit struct {
	// Capacity is the most tokens the bucket holds: the largest burst.
	Capacity int

	// RefillRate is how many tokens return to the bucket per second.
	RefillRate float64
}

// Validate reports whether the limit describes a usable bucket. The engine
// tolerates invalid limits without panicking, but stores refuse them, so a
// misconfiguration fails loudly instead of quietly allowing or refusing
// everything.
func (l Limit) Validate() error {
	if l.Capacity < 1 {
		return fmt.Errorf("%w: capacity must be at least 1, got %d", ErrInvalidLimit, l.Capacity)
	}
	if !(l.RefillRate > 0) || math.IsInf(l.RefillRate, 1) {
		return fmt.Errorf("%w: refill rate must be positive and finite, got %v", ErrInvalidLimit, l.RefillRate)
	}
	return nil
}

// Bucket is the state of one bucket. The zero Bucket has never been used
// and is treated as full.
type Bucket struct {
	// Tokens is the balance as of LastRefill. It is fractional: refill is
	// continuous.
	Tokens float64

	// LastRefill is when Tokens was last brought up to date.
	LastRefill time.Time
}

// Decision is the outcome of one request against a bucket (ADR 0011).
type Decision struct {
	// Allowed reports whether the request may proceed.
	Allowed bool

	// Limit is the bucket's capacity.
	Limit int

	// Remaining is how many whole tokens are left after this decision.
	Remaining int

	// ResetAfter is how long until the bucket is full again if nothing
	// else uses it, or zero when it already is.
	ResetAfter time.Duration

	// RetryAfter is how long until a request would be allowed. It is zero
	// when this one was.
	RetryAfter time.Duration
}

// Take refills the bucket to now and tries to take one token from it. It
// returns the bucket's new state and the decision. The caller stores the
// state; Take changes nothing itself.
func Take(b Bucket, l Limit, now time.Time) (Bucket, Decision) {
	b = refill(b, l, now)
	if b.Tokens < 1-wholeTokenTolerance || l.Capacity < 1 {
		return b, decide(b, l, now, false)
	}
	b.Tokens = max(b.Tokens-1, 0)
	return b, decide(b, l, now, true)
}

// Peek reports what Take would decide at now, without taking a token. Its
// Remaining counts the tokens a Take would start from.
func Peek(b Bucket, l Limit, now time.Time) Decision {
	b = refill(b, l, now)
	allowed := b.Tokens >= 1-wholeTokenTolerance && l.Capacity >= 1
	return decide(b, l, now, allowed)
}

// refill brings the balance up to date at now. A new bucket is full. Zero or
// negative elapsed time refills nothing, and a clock that went backwards
// leaves LastRefill where it was, so the interval is not counted twice when
// the clock catches up. A balance above the capacity, because the capacity
// was lowered, is clamped.
func refill(b Bucket, l Limit, now time.Time) Bucket {
	capacity := float64(max(l.Capacity, 0))
	if b.LastRefill.IsZero() {
		return Bucket{Tokens: capacity, LastRefill: now}
	}

	tokens := b.Tokens
	if !(tokens >= 0) { // also catches NaN, which only corrupt state could hold
		tokens = 0
	}
	tokens = min(tokens, capacity)

	elapsed := now.Sub(b.LastRefill)
	if elapsed <= 0 {
		return Bucket{Tokens: tokens, LastRefill: b.LastRefill}
	}
	if l.RefillRate > 0 {
		tokens = min(capacity, tokens+elapsed.Seconds()*l.RefillRate)
	}
	return Bucket{Tokens: tokens, LastRefill: now}
}

// decide describes a refilled bucket b after the decision has been applied
// to it, as seen at now.
func decide(b Bucket, l Limit, now time.Time, allowed bool) Decision {
	// After the clock has gone backwards, b.LastRefill is ahead of now and
	// refill only resumes once now passes it, so durations measured from
	// now must include that lag or they would come out too short.
	lag := max(b.LastRefill.Sub(now), 0)

	capacity := max(l.Capacity, 0)
	d := Decision{
		Allowed:   allowed,
		Limit:     capacity,
		Remaining: min(int(math.Floor(b.Tokens+wholeTokenTolerance)), capacity),
	}
	if toFull := timeToRefill(float64(capacity)-b.Tokens, l.RefillRate); toFull > 0 {
		d.ResetAfter = addSaturating(lag, toFull)
	}
	if !allowed {
		d.RetryAfter = addSaturating(lag, timeToRefill(1-b.Tokens, l.RefillRate))
		if l.Capacity < 1 {
			// A bucket that holds no tokens never admits anyone.
			d.RetryAfter = math.MaxInt64
		}
	}
	return d
}

// addSaturating adds two non-negative durations, stopping at the largest
// Duration instead of overflowing.
func addSaturating(a, b time.Duration) time.Duration {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// maxDurationNanos is the largest time.Duration as a float64. The conversion
// rounds it up to exactly 2^63, so a nanosecond count must be strictly
// below it to fit in an int64.
const maxDurationNanos = float64(math.MaxInt64)

// timeToRefill returns how long it takes to refill the given number of
// tokens at rate, rounded up to the nanosecond so that waiting that long is
// always enough. It saturates at the largest Duration instead of
// overflowing, and a rate that refills nothing never gets there.
func timeToRefill(tokens, rate float64) time.Duration {
	if tokens <= wholeTokenTolerance {
		return 0
	}
	if !(rate > 0) {
		return math.MaxInt64
	}
	nanos := math.Ceil(tokens / rate * float64(time.Second))
	if !(nanos < maxDurationNanos) { // also catches +Inf and NaN
		return math.MaxInt64
	}
	return time.Duration(nanos)
}
