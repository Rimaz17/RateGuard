package domain

import "time"

// Policy is a named rate limit, such as Free, Premium or Enterprise, that is
// assigned to clients.
type Policy struct {
	ID   string
	Name string

	// Capacity is the bucket size: the most requests allowed in a burst.
	Capacity int

	// WindowSeconds is the period the capacity is quoted over, as in
	// "100 requests per 60 seconds".
	WindowSeconds int

	// RefillRate is how many tokens return to the bucket per second. It is
	// Capacity / WindowSeconds unless set explicitly (ADR 0009).
	RefillRate float64

	// Enabled is false for a policy that has been switched off. Policies
	// are disabled rather than deleted so that clients never point at a
	// missing row.
	Enabled bool

	// Default marks the one policy that applies to callers without an
	// enabled policy of their own, including callers identified by IP
	// (ADR 0009). At most one policy is the default.
	Default bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPolicy holds the caller-supplied fields of a policy that does not
// exist yet; storage assigns the ID and timestamps.
type NewPolicy struct {
	Name          string
	Capacity      int
	WindowSeconds int
	RefillRate    float64
	Enabled       bool

	// Default makes the new policy the default, taking the flag from
	// whichever policy held it.
	Default bool
}

// PolicyUpdate changes some fields of a policy. A nil field is left as it
// is. When Capacity or WindowSeconds changes and RefillRate is nil, storage
// recomputes the refill rate as Capacity / WindowSeconds, so the two never
// silently disagree (ADR 0009).
type PolicyUpdate struct {
	Name          *string
	Capacity      *int
	WindowSeconds *int
	RefillRate    *float64
	Enabled       *bool

	// Default true makes the policy the default, taking the flag from
	// whichever policy held it. Default false clears it.
	Default *bool
}

// DefaultRefillRate is the refill rate that spreads capacity evenly over
// the window: 100 per 60 seconds refills 100/60 tokens a second.
func DefaultRefillRate(capacity, windowSeconds int) float64 {
	return float64(capacity) / float64(windowSeconds)
}
