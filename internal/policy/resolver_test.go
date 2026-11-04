package policy_test

import (
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/policy"
)

const (
	freeID       = "00000000-0000-7000-8000-000000000001"
	premiumID    = "00000000-0000-7000-8000-000000000002"
	enterpriseID = "00000000-0000-7000-8000-000000000003"

	customerApp   = "00000000-0000-7000-8000-0000000000c1"
	merchantApp   = "00000000-0000-7000-8000-0000000000c2"
	adminApp      = "00000000-0000-7000-8000-0000000000c3"
	unassignedApp = "00000000-0000-7000-8000-0000000000c4"
)

func keyed(clientID string) domain.Identity {
	return domain.Identity{Kind: domain.IdentityAPIKey, ClientID: clientID, KeyPrefix: "rg_tst_aaaaaaaaaaaaaaaa"}
}

var anonymous = domain.Identity{Kind: domain.IdentityIP, IP: netip.MustParseAddr("203.0.113.7")}

// plans is the table from the project overview: Free, Premium and
// Enterprise, each with one endpoint override. Free is the default.
func plans() *fakeStore {
	s := newFakeStore()
	s.addPolicy(freeID, "Free", 100, rule("login", http.MethodPost, "/api/login", 5))
	s.addPolicy(premiumID, "Premium", 1000, rule("orders", domain.AnyMethod, "/api/orders/**", 20))
	s.addPolicy(enterpriseID, "Enterprise", 5000, rule("products", http.MethodGet, "/api/products/*", 100))
	s.set(func(s *fakeStore) {
		s.clients[customerApp] = freeID
		s.clients[merchantApp] = premiumID
		s.clients[adminApp] = enterpriseID
		s.clients[unassignedApp] = ""
		s.defaultID = freeID
	})
	return s
}

func TestResolvePlansPerRequest(t *testing.T) {
	s := plans()
	r := policy.NewResolver(s, s)
	tests := []struct {
		name         string
		id           domain.Identity
		method, path string
		wantPolicy   string
		wantRule     string
		wantCapacity int
	}{
		{"free login is the override", keyed(customerApp), http.MethodPost, "/api/login", "Free", "login", 5},
		{"free login override is POST only", keyed(customerApp), http.MethodGet, "/api/login", "Free", "", 100},
		{"methods are compared in upper case", keyed(customerApp), "post", "/api/login", "Free", "login", 5},
		{"trailing slash cannot dodge the override", keyed(customerApp), http.MethodPost, "/api/login/", "Free", "login", 5},
		{"doubled slash cannot dodge the override", keyed(customerApp), http.MethodPost, "//api//login", "Free", "login", 5},
		{"dot segments cannot dodge the override", keyed(customerApp), http.MethodPost, "/api/x/../login", "Free", "login", 5},
		{"free elsewhere is the plan limit", keyed(customerApp), http.MethodGet, "/api/products", "Free", "", 100},
		{"premium orders override", keyed(merchantApp), http.MethodDelete, "/api/orders/42/items", "Premium", "orders", 20},
		{"premium orders root", keyed(merchantApp), http.MethodGet, "/api/orders", "Premium", "orders", 20},
		{"premium login has no override", keyed(merchantApp), http.MethodPost, "/api/login", "Premium", "", 1000},
		{"enterprise product override", keyed(adminApp), http.MethodGet, "/api/products/7", "Enterprise", "products", 100},
		{"enterprise override covers one segment only", keyed(adminApp), http.MethodGet, "/api/products/7/reviews", "Enterprise", "", 5000},
		{"unassigned client gets the default", keyed(unassignedApp), http.MethodPost, "/api/login", "Free", "login", 5},
		{"caller known by IP gets the default", anonymous, http.MethodGet, "/", "Free", "", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(t.Context(), tt.id, tt.method, tt.path)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.PolicyName != tt.wantPolicy || got.RuleID != tt.wantRule || got.Capacity != tt.wantCapacity {
				t.Errorf("Resolve = %s rule %q capacity %d, want %s rule %q capacity %d",
					got.PolicyName, got.RuleID, got.Capacity, tt.wantPolicy, tt.wantRule, tt.wantCapacity)
			}
			if got.WindowSeconds != 60 || got.RefillRate != float64(tt.wantCapacity)/60 {
				t.Errorf("window %d, refill %v; want 60 and %v", got.WindowSeconds, got.RefillRate, float64(tt.wantCapacity)/60)
			}
		})
	}
}

func TestResolveReturnsTheWholeLimit(t *testing.T) {
	s := newFakeStore()
	s.set(func(s *fakeStore) {
		s.policies[freeID] = domain.Policy{ID: freeID, Name: "Burst", Capacity: 50, WindowSeconds: 10, RefillRate: 0.5, Enabled: true}
		s.rules[freeID] = []domain.EndpointRule{{ID: "r", PolicyID: freeID, Method: "*", PathPattern: "/x", Capacity: 6, WindowSeconds: 3}}
		s.defaultID = freeID
	})
	r := policy.NewResolver(s, s)

	got, err := r.Resolve(t.Context(), anonymous, http.MethodGet, "/y")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ResolvedLimit{PolicyID: freeID, PolicyName: "Burst", Capacity: 50, WindowSeconds: 10, RefillRate: 0.5}
	if got != want {
		t.Errorf("policy limit = %+v, want %+v (an explicit refill rate is kept)", got, want)
	}

	got, err = r.Resolve(t.Context(), anonymous, http.MethodGet, "/x")
	if err != nil {
		t.Fatal(err)
	}
	want = domain.ResolvedLimit{PolicyID: freeID, PolicyName: "Burst", RuleID: "r", Capacity: 6, WindowSeconds: 3, RefillRate: 2}
	if got != want {
		t.Errorf("rule limit = %+v, want %+v", got, want)
	}
}

func TestResolveFallsBackToTheDefault(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *fakeStore)
		id     domain.Identity
		want   string // policy name, or "" for ErrNoPolicy
	}{
		{"assigned policy disabled", func(s *fakeStore) { disable(s, premiumID) }, keyed(merchantApp), "Free"},
		{"no default, unassigned client", func(s *fakeStore) { s.defaultID = "" }, keyed(unassignedApp), ""},
		{"no default, caller known by IP", func(s *fakeStore) { s.defaultID = "" }, anonymous, ""},
		{"no default, assigned policy still applies", func(s *fakeStore) { s.defaultID = "" }, keyed(merchantApp), "Premium"},
		{"default disabled", func(s *fakeStore) { disable(s, freeID) }, anonymous, ""},
		{"assigned and default both disabled", func(s *fakeStore) { disable(s, premiumID); disable(s, freeID) }, keyed(merchantApp), ""},
		{"default can be any policy", func(s *fakeStore) { s.defaultID = enterpriseID }, anonymous, "Enterprise"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := plans()
			s.set(tt.change)
			got, err := policy.NewResolver(s, s).Resolve(t.Context(), tt.id, http.MethodGet, "/")
			if tt.want == "" {
				if !errors.Is(err, domain.ErrNoPolicy) {
					t.Errorf("Resolve = %+v, %v; want ErrNoPolicy", got, err)
				}
				return
			}
			if err != nil || got.PolicyName != tt.want {
				t.Errorf("Resolve = %q, %v; want %q", got.PolicyName, err, tt.want)
			}
		})
	}
}

func disable(s *fakeStore, id string) {
	p := s.policies[id]
	p.Enabled = false
	s.policies[id] = p
}

func TestResolveErrors(t *testing.T) {
	failure := errors.New("database is down")
	tests := []struct {
		name   string
		change func(s *fakeStore)
		id     domain.Identity
		want   error
	}{
		{"source failure", func(s *fakeStore) { s.err = failure }, keyed(customerApp), failure},
		{"source failure for an anonymous caller", func(s *fakeStore) { s.err = failure }, anonymous, failure},
		{"unknown client", nil, keyed("00000000-0000-7000-8000-0000000000ff"), domain.ErrClientNotFound},
		{"assigned policy missing", func(s *fakeStore) { delete(s.policies, premiumID) }, keyed(merchantApp), domain.ErrPolicyNotFound},
		{"stored rule with an invalid pattern", func(s *fakeStore) { s.rules[freeID] = []domain.EndpointRule{rule("bad", "*", "no-slash", 1)} }, anonymous, domain.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := plans()
			if tt.change != nil {
				s.set(tt.change)
			}
			_, err := policy.NewResolver(s, s).Resolve(t.Context(), tt.id, http.MethodGet, "/")
			if !errors.Is(err, tt.want) {
				t.Errorf("Resolve error = %v, want %v", err, tt.want)
			}
			if errors.Is(err, domain.ErrNoPolicy) {
				t.Errorf("a failure must not read as ErrNoPolicy: %v", err)
			}
		})
	}

	s := plans()
	_, err := policy.NewResolver(s, s).Resolve(t.Context(), domain.Identity{Kind: "carrier pigeon"}, http.MethodGet, "/")
	if err == nil {
		t.Error("an unknown identity kind resolved")
	}
}

// TestResolvePrecedence checks ADR 0010: when several rules match, the
// most specific pattern wins, compared left to right, then a named method
// beats any method. The outcome must not depend on the order rules were
// created in.
func TestResolvePrecedence(t *testing.T) {
	rules := []domain.EndpointRule{
		rule("api-rest", "*", "/api/**", 1),
		rule("api-any", "*", "/api/*", 2),
		rule("login-any", "*", "/api/login", 3),
		rule("login-post", http.MethodPost, "/api/login", 4),
		rule("api-exact", "*", "/api", 5),
		rule("items-get", http.MethodGet, "/api/*/items", 6),
		rule("orders-rest", "*", "/api/orders/**", 7),
		rule("everything", "*", "/**", 8),
		rule("orders-any-get", http.MethodGet, "/api/orders/*", 9),
	}
	tests := []struct {
		method, path, want string
	}{
		{http.MethodPost, "/api/login", "login-post"},
		{http.MethodGet, "/api/login", "login-any"},
		{http.MethodGet, "/api/x", "api-any"},
		{http.MethodGet, "/api", "api-exact"},
		{http.MethodGet, "/api/orders/1/items", "orders-rest"},
		{http.MethodGet, "/api/orders/1", "orders-any-get"},
		{http.MethodPost, "/api/orders/1", "orders-rest"},
		{http.MethodGet, "/api/foo/items", "items-get"},
		{http.MethodPost, "/api/foo/items", "api-rest"},
		{http.MethodGet, "/api/a/b/c", "api-rest"},
		{http.MethodGet, "/other", "everything"},
		{http.MethodGet, "/", "everything"},
	}

	orders := [][]domain.EndpointRule{rules, reversed(rules), rotated(rules, 4)}
	for i, ordered := range orders {
		s := newFakeStore()
		s.addPolicy(freeID, "Free", 100, ordered...)
		s.set(func(s *fakeStore) { s.defaultID = freeID })
		r := policy.NewResolver(s, s)
		for _, tt := range tests {
			got, err := r.Resolve(t.Context(), anonymous, tt.method, tt.path)
			if err != nil {
				t.Fatalf("order %d, %s %s: %v", i, tt.method, tt.path, err)
			}
			if got.RuleID != tt.want {
				t.Errorf("order %d, %s %s: rule %q won, want %q", i, tt.method, tt.path, got.RuleID, tt.want)
			}
		}
	}
}

func reversed(rs []domain.EndpointRule) []domain.EndpointRule {
	out := slices.Clone(rs)
	slices.Reverse(out)
	return out
}

func rotated(rs []domain.EndpointRule, n int) []domain.EndpointRule {
	return append(slices.Clone(rs[n:]), rs[:n]...)
}
