package limiter

import (
	"context"
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Resolver works out which limit applies to a request. *policy.Resolver
// satisfies it, from its in-process cache (ADR 0005).
type Resolver interface {
	Resolve(ctx context.Context, id domain.Identity, method, requestPath string) (domain.ResolvedLimit, error)
}

// Service makes rate-limit decisions: it resolves the limit that applies to
// a request, finds the caller's bucket for it, and asks the store. It does
// no I/O of its own, so the hot path costs one cache lookup and one store
// call (ADR 0004).
type Service struct {
	resolver Resolver
	store    Store
}

// New returns a Service that resolves limits with resolver and keeps
// buckets in store.
func New(resolver Resolver, store Store) *Service {
	return &Service{resolver: resolver, store: store}
}

// Check decides a request from the caller id to method and requestPath,
// taking a token from its bucket if one is available. It fails with
// domain.ErrNoPolicy when no enabled policy applies to the caller, which
// must be treated as a refusal, never as unlimited (ADR 0009).
func (s *Service) Check(ctx context.Context, id domain.Identity, method, requestPath string) (Decision, error) {
	return s.decide(ctx, id, method, requestPath, false)
}

// Peek reports what Check would decide, without taking a token.
func (s *Service) Peek(ctx context.Context, id domain.Identity, method, requestPath string) (Decision, error) {
	return s.decide(ctx, id, method, requestPath, true)
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
