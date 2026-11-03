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

	// RefillRate is how many tokens return to the bucket per second.
	RefillRate float64

	// Enabled is false for a policy that has been switched off. Policies
	// are disabled rather than deleted so that clients never point at a
	// missing row.
	Enabled bool

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
}
