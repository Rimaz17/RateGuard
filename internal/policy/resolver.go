package policy

import (
	"context"
	"fmt"
	"time"

	"github.com/Rimaz17/RateGuard/internal/cache"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// DefaultTTL is how long a cached entry lives without an invalidation. It is
// a safety net for a missed pub/sub message, not the primary mechanism
// (ADR 0005).
const DefaultTTL = 10 * time.Second

// defaultLoadTimeout bounds one shared cache load from Postgres.
const defaultLoadTimeout = 5 * time.Second

// defaultKey is the default-policy cache's only key.
const defaultKey = "default"

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

// Resolver works out the limit that applies to a request. It caches what it
// reads (ADR 0005), so after warming up a decision touches no database:
//
//   - each client's policy ID, dropped by a client change;
//   - each policy, compiled with its rules, dropped by a policy change;
//   - the default policy's ID, dropped by any policy change, since any
//     policy write may move the default flag.
//
// A Resolver is safe for concurrent use.
type Resolver struct {
	clients  *cache.Cache[string]
	policies *cache.Cache[*compiledPolicy]
	defaults *cache.Cache[string]
}

// Option configures a Resolver.
type Option func(*options)

type options struct {
	ttl time.Duration
	now func() time.Time
}

// WithTTL sets how long cached entries live. The default is DefaultTTL.
func WithTTL(ttl time.Duration) Option {
	return func(o *options) { o.ttl = ttl }
}

// WithClock sets the clock that expires cached entries. It exists for
// tests; the default is time.Now. It never touches bucket arithmetic, which
// runs on Redis's clock (ADR 0002).
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// NewResolver returns a Resolver that reads from the given sources.
func NewResolver(clients Assignments, policies Catalog, opts ...Option) *Resolver {
	o := options{ttl: DefaultTTL, now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return &Resolver{
		clients: cache.New(o.ttl, defaultLoadTimeout, o.now, clients.PolicyID),
		policies: cache.New(o.ttl, defaultLoadTimeout, o.now, func(ctx context.Context, id string) (*compiledPolicy, error) {
			p, rules, err := policies.GetWithRules(ctx, id)
			if err != nil {
				return nil, err
			}
			return compile(p, rules)
		}),
		defaults: cache.New(o.ttl, defaultLoadTimeout, o.now, func(ctx context.Context, _ string) (string, error) {
			return policies.DefaultID(ctx)
		}),
	}
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
		policyID, err := r.clients.Get(ctx, id.ClientID)
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

	defaultID, err := r.defaults.Get(ctx, defaultKey)
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
	cp, err := r.policies.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("loading policy %s: %w", id, err)
	}
	return cp, nil
}

// Invalidate drops whatever the change may have made stale.
func (r *Resolver) Invalidate(c Change) {
	switch c.Kind {
	case KindClient:
		r.clients.Invalidate(c.ID)
	case KindPolicy:
		r.policies.Invalidate(c.ID)
		r.defaults.Invalidate(defaultKey)
	}
}

// Flush drops everything. It is for when invalidations may have been missed,
// such as after the pub/sub connection drops and reconnects.
func (r *Resolver) Flush() {
	r.clients.Flush()
	r.policies.Flush()
	r.defaults.Flush()
}
