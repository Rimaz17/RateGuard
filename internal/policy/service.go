package policy

import (
	"context"
	"fmt"
	"strings"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Store persists policies.
type Store interface {
	Create(ctx context.Context, np domain.NewPolicy) (domain.Policy, error)
	Get(ctx context.Context, id string) (domain.Policy, error)
	List(ctx context.Context, page domain.Page) ([]domain.Policy, error)
	Update(ctx context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error)
}

// RuleStore persists endpoint rules. Create must enforce maxRules
// atomically.
type RuleStore interface {
	Create(ctx context.Context, policyID string, nr domain.NewEndpointRule, maxRules int) (domain.EndpointRule, error)
	List(ctx context.Context, policyID string) ([]domain.EndpointRule, error)
	Delete(ctx context.Context, policyID, ruleID string) error
}

// Assigner records which policy a client is on.
type Assigner interface {
	AssignPolicy(ctx context.Context, clientID, policyID string) (domain.Client, error)
}

// Notifier tells every instance that cached resolution data is stale. It
// is called after a write has committed, and it does not fail the write:
// an instance that misses the notice catches up within the cache TTL.
type Notifier interface {
	Notify(ctx context.Context, c Change)
}

// Service manages policies, their endpoint rules and clients' assignments,
// and announces every change so caches everywhere drop it (ADR 0005).
type Service struct {
	policies Store
	rules    RuleStore
	clients  Assigner
	notifier Notifier
}

// New returns a Service on the given stores.
func New(policies Store, rules RuleStore, clients Assigner, notifier Notifier) *Service {
	return &Service{policies: policies, rules: rules, clients: clients, notifier: notifier}
}

// Create adds a policy. A zero refill rate means capacity / window_seconds
// (ADR 0009). A name already in use is domain.ErrPolicyNameTaken.
func (s *Service) Create(ctx context.Context, np domain.NewPolicy) (domain.Policy, error) {
	if np.RefillRate == 0 {
		np.RefillRate = domain.DefaultRefillRate(np.Capacity, np.WindowSeconds)
	}
	p, err := s.policies.Create(ctx, np)
	if err != nil {
		return domain.Policy{}, fmt.Errorf("creating policy: %w", err)
	}
	// A new policy is in no cache yet, but if it took the default flag the
	// cached default is stale.
	s.notifier.Notify(ctx, ForPolicy(p.ID))
	return p, nil
}

// Get returns one policy, or domain.ErrPolicyNotFound.
func (s *Service) Get(ctx context.Context, id string) (domain.Policy, error) {
	p, err := s.policies.Get(ctx, id)
	if err != nil {
		return domain.Policy{}, fmt.Errorf("getting policy: %w", err)
	}
	return p, nil
}

// List returns one page of policies in creation order.
func (s *Service) List(ctx context.Context, page domain.Page) ([]domain.Policy, error) {
	ps, err := s.policies.List(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("listing policies: %w", err)
	}
	return ps, nil
}

// Update changes the given fields of a policy; see domain.PolicyUpdate.
func (s *Service) Update(ctx context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error) {
	p, err := s.policies.Update(ctx, id, u)
	if err != nil {
		return domain.Policy{}, fmt.Errorf("updating policy: %w", err)
	}
	s.notifier.Notify(ctx, ForPolicy(p.ID))
	return p, nil
}

// Disable switches a policy off. Its clients fall back to the default
// policy (ADR 0009). Disabling a disabled policy succeeds.
func (s *Service) Disable(ctx context.Context, id string) (domain.Policy, error) {
	disabled := false
	p, err := s.policies.Update(ctx, id, domain.PolicyUpdate{Enabled: &disabled})
	if err != nil {
		return domain.Policy{}, fmt.Errorf("disabling policy: %w", err)
	}
	s.notifier.Notify(ctx, ForPolicy(p.ID))
	return p, nil
}

// CreateRule adds an endpoint rule to a policy. The method is upper-cased
// and must be an HTTP method token or "*"; the pattern must follow ADR
// 0010. Both are reported as *domain.ValidationError.
func (s *Service) CreateRule(ctx context.Context, policyID string, nr domain.NewEndpointRule) (domain.EndpointRule, error) {
	nr.Method = strings.ToUpper(nr.Method)
	if !validMethod(nr.Method) {
		return domain.EndpointRule{}, &domain.ValidationError{Field: "method", Reason: `must be an HTTP method such as GET, or "*" for any`}
	}
	if _, err := ParsePattern(nr.PathPattern); err != nil {
		return domain.EndpointRule{}, err
	}
	r, err := s.rules.Create(ctx, policyID, nr, domain.MaxEndpointRules)
	if err != nil {
		return domain.EndpointRule{}, fmt.Errorf("creating endpoint rule: %w", err)
	}
	s.notifier.Notify(ctx, ForPolicy(policyID))
	return r, nil
}

// ListRules returns a policy's endpoint rules in creation order. An unknown
// policy is domain.ErrPolicyNotFound rather than an empty list.
func (s *Service) ListRules(ctx context.Context, policyID string) ([]domain.EndpointRule, error) {
	if _, err := s.policies.Get(ctx, policyID); err != nil {
		return nil, fmt.Errorf("listing endpoint rules: %w", err)
	}
	rules, err := s.rules.List(ctx, policyID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoint rules: %w", err)
	}
	return rules, nil
}

// DeleteRule removes one of a policy's endpoint rules.
func (s *Service) DeleteRule(ctx context.Context, policyID, ruleID string) error {
	if err := s.rules.Delete(ctx, policyID, ruleID); err != nil {
		return fmt.Errorf("deleting endpoint rule: %w", err)
	}
	s.notifier.Notify(ctx, ForPolicy(policyID))
	return nil
}

// AssignPolicy puts a client on a policy, or takes it off any policy when
// policyID is "". A disabled policy cannot be assigned
// (domain.ErrPolicyDisabled).
func (s *Service) AssignPolicy(ctx context.Context, clientID, policyID string) (domain.Client, error) {
	c, err := s.clients.AssignPolicy(ctx, clientID, policyID)
	if err != nil {
		return domain.Client{}, fmt.Errorf("assigning policy: %w", err)
	}
	s.notifier.Notify(ctx, ForClient(c.ID))
	return c, nil
}

// validMethod reports whether m is domain.AnyMethod or an upper-case HTTP
// method token of at most 16 letters.
func validMethod(m string) bool {
	if m == domain.AnyMethod {
		return true
	}
	if m == "" || len(m) > 16 {
		return false
	}
	for i := range len(m) {
		if m[i] < 'A' || m[i] > 'Z' {
			return false
		}
	}
	return true
}
