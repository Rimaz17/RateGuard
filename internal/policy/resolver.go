package policy

import (
	"context"
	"fmt"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Assignments reads which policy each client is assigned.
type Assignments interface {
	// PolicyID returns the client's policy ID, or "" when it has none.
	PolicyID(ctx context.Context, clientID string) (string, error)
}

// Catalog reads policies with their endpoint rules, and which policy
// is the default.
type Catalog interface {
	GetWithRules(ctx context.Context, id string) (domain.Policy, []domain.EndpointRule, error)

	// DefaultID returns the default policy's ID, or "" when there is none.
	DefaultID(ctx context.Context) (string, error)
}

// Resolver works out the limit that applies to a request.
type Resolver struct {
	clients  Assignments
	policies Catalog
}

// NewResolver returns a Resolver that reads from the given sources.
func NewResolver(clients Assignments, policies Catalog) *Resolver {
	return &Resolver{clients: clients, policies: policies}
}

// Resolve returns the limit for a request from the caller id to method and
// requestPath (ADR 0009):
//
//  1. A caller with an API key uses its client's policy, if it has one and
//     the policy is enabled.
//  2. Otherwise the caller, keyed or not, uses the default policy, if there
//     is one and it is enabled.
//  3. Otherwise there is no policy, and the error is domain.ErrNoPolicy.
//
// Within the chosen policy, the most specific matching endpoint rule beats
// the policy's own limit (ADR 0010).
func (r *Resolver) Resolve(ctx context.Context, id domain.Identity, method, requestPath string) (domain.ResolvedLimit, error) {
	switch id.Kind {
	case domain.IdentityAPIKey:
		policyID, err := r.clients.PolicyID(ctx, id.ClientID)
		if err != nil {
			return domain.ResolvedLimit{}, fmt.Errorf("resolving policy of client %s: %w", id.ClientID, err)
		}
		if policyID != "" {
			cp, err := r.policy(ctx, policyID)
			if err != nil {
				return domain.ResolvedLimit{}, err
			}
			if cp.enabled {
				return cp.limitFor(method, requestPath), nil
			}
		}
	case domain.IdentityIP:
	default:
		return domain.ResolvedLimit{}, fmt.Errorf("resolving policy: unknown identity kind %q", id.Kind)
	}

	defaultID, err := r.policies.DefaultID(ctx)
	if err != nil {
		return domain.ResolvedLimit{}, fmt.Errorf("resolving default policy: %w", err)
	}
	if defaultID == "" {
		return domain.ResolvedLimit{}, domain.ErrNoPolicy
	}
	cp, err := r.policy(ctx, defaultID)
	if err != nil {
		return domain.ResolvedLimit{}, err
	}
	if !cp.enabled {
		return domain.ResolvedLimit{}, domain.ErrNoPolicy
	}
	return cp.limitFor(method, requestPath), nil
}

func (r *Resolver) policy(ctx context.Context, id string) (*compiledPolicy, error) {
	p, rules, err := r.policies.GetWithRules(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading policy %s: %w", id, err)
	}
	cp, err := compile(p, rules)
	if err != nil {
		return nil, fmt.Errorf("loading policy %s: %w", id, err)
	}
	return cp, nil
}
