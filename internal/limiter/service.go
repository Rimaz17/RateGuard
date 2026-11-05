package limiter

import (
	"context"
	"errors"
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Resolver works out which limit applies to a request. *policy.Resolver
// satisfies it, from its in-process cache (ADR 0005).
type Resolver interface {
	Resolve(ctx context.Context, id domain.Identity, method, requestPath string) (domain.ResolvedLimit, error)
}

// Outcome is how a check ended, as the decision metrics count it.
type Outcome string

// Check outcomes.
const (
	OutcomeAllowed  Outcome = "allowed"
	OutcomeRejected Outcome = "rejected"
	OutcomeNoPolicy Outcome = "no_policy"
	OutcomeError    Outcome = "error"
)

// OutcomeCounter counts checks by outcome. *Metrics satisfies it. It runs on
// the decision path and must be cheap and non-blocking.
type OutcomeCounter interface {
	Checked(o Outcome)
}

// UsageRecorder counts each caller's allowed and rejected checks for
// analytics. *analytics.Recorder satisfies it. It runs on the decision path
// and must do no I/O (ADR 0004).
type UsageRecorder interface {
	// Record counts one check by clientID, or by a caller identified only
	// by IP when clientID is "".
	Record(clientID string, allowed bool)
}

// Option configures a Service.
type Option func(*Service)

// WithUsage records every decided check in u.
func WithUsage(u UsageRecorder) Option {
	return func(s *Service) { s.usage = u }
}

// WithOutcomes counts every check's outcome in c.
func WithOutcomes(c OutcomeCounter) Option {
	return func(s *Service) { s.outcomes = c }
}

// Service makes rate-limit decisions: it resolves the limit that applies to
// a request, finds the caller's bucket for it, and asks the store. It does
// no I/O of its own, so the hot path costs one cache lookup and one store
// call (ADR 0004). Usage and outcomes are counted in memory only.
type Service struct {
	resolver Resolver
	store    Store
	usage    UsageRecorder
	outcomes OutcomeCounter
}

// New returns a Service that resolves limits with resolver and keeps
// buckets in store.
func New(resolver Resolver, store Store, opts ...Option) *Service {
	s := &Service{resolver: resolver, store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Check decides a request from the caller id to method and requestPath,
// taking a token from its bucket if one is available. It fails with
// domain.ErrNoPolicy when no enabled policy applies to the caller, which
// must be treated as a refusal, never as unlimited (ADR 0009).
//
// A decided check is counted for analytics, and every check's outcome for
// metrics, except a check whose caller went away before it was decided.
func (s *Service) Check(ctx context.Context, id domain.Identity, method, requestPath string) (Decision, error) {
	d, err := s.decide(ctx, id, method, requestPath, false)
	switch {
	case err == nil:
		if s.usage != nil {
			s.usage.Record(id.ClientID, d.Allowed)
		}
		if d.Allowed {
			s.count(OutcomeAllowed)
		} else {
			s.count(OutcomeRejected)
		}
	case errors.Is(err, domain.ErrNoPolicy):
		s.count(OutcomeNoPolicy)
	case ctx.Err() != nil:
		// Nobody is waiting for this decision; it is no outcome at all.
	default:
		s.count(OutcomeError)
	}
	return d, err
}

// Peek reports what Check would decide, without taking a token. Peeks are
// not counted: they decide nothing.
func (s *Service) Peek(ctx context.Context, id domain.Identity, method, requestPath string) (Decision, error) {
	return s.decide(ctx, id, method, requestPath, true)
}

func (s *Service) count(o Outcome) {
	if s.outcomes != nil {
		s.outcomes.Checked(o)
	}
}

func (s *Service) decide(ctx context.Context, id domain.Identity, method, requestPath string, peek bool) (Decision, error) {
	caller, err := callerID(id)
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: %w", err)
	}
	limit, err := s.resolver.Resolve(ctx, id, method, requestPath)
	if err != nil {
		return Decision{}, fmt.Errorf("resolving the limit: %w", err)
	}
	// Every path a rule matches shares the rule's bucket, and everything
	// else shares the policy's, so the rule is the bucket's scope
	// (ADR 0010). The policy is not: a client keeps its bucket when its
	// plan changes, and the new limit applies to it (ADR 0011).
	key := keyFor(caller, limit.RuleID)
	return call(ctx, s.store, key, Limit{Capacity: limit.Capacity, RefillRate: limit.RefillRate}, peek)
}
