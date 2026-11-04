package domain

import "time"

// MaxEndpointRules is how many endpoint rules one policy may hold. Rules are
// scanned in memory on every decision, so the list stays short (ADR 0010).
const MaxEndpointRules = 100

// AnyMethod is the method of an endpoint rule that applies to every HTTP
// method.
const AnyMethod = "*"

// EndpointRule gives part of an API its own limit within a policy, such as
// 5 requests a minute on /api/login. A matching rule beats the policy's own
// limit; ADR 0010 defines the pattern syntax and which rule wins when
// several match.
type EndpointRule struct {
	ID       string
	PolicyID string

	// Method is an upper-case HTTP method, or AnyMethod.
	Method string

	// PathPattern is a path whose segments may be * (exactly one segment)
	// or, as the last segment, ** (zero or more segments).
	PathPattern string

	Capacity      int
	WindowSeconds int
	CreatedAt     time.Time
}

// RefillRate is the rule's tokens per second: its capacity spread evenly
// over its window.
func (r EndpointRule) RefillRate() float64 {
	return DefaultRefillRate(r.Capacity, r.WindowSeconds)
}

// NewEndpointRule holds the caller-supplied fields of a rule that does not
// exist yet.
type NewEndpointRule struct {
	Method        string
	PathPattern   string
	Capacity      int
	WindowSeconds int
}

// ResolvedLimit is the limit that applies to one request: the caller's
// policy, narrowed by the endpoint rule that matched, if any.
type ResolvedLimit struct {
	PolicyID   string
	PolicyName string

	// RuleID is the endpoint rule that matched, or "" when the policy's own
	// limit applies. Every path a rule matches shares one bucket per
	// caller, so the rule, not the path, scopes the bucket.
	RuleID string

	Capacity      int
	WindowSeconds int
	RefillRate    float64
}
