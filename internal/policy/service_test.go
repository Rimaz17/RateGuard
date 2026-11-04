package policy_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/policy"
)

// The repositories behind the service are stubbed: these tests cover what
// the service adds (defaults, validation and change notices). The real
// repositories on a real database are covered in test/integration.

type recordingNotifier struct {
	mu      sync.Mutex
	changes []policy.Change
}

func (n *recordingNotifier) Notify(_ context.Context, c policy.Change) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changes = append(n.changes, c)
}

func (n *recordingNotifier) got() []policy.Change {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.changes)
}

type stubPolicies struct {
	created domain.NewPolicy
	updated domain.PolicyUpdate
	err     error
}

func (s *stubPolicies) Create(_ context.Context, np domain.NewPolicy) (domain.Policy, error) {
	s.created = np
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{ID: freeID, Name: np.Name, Capacity: np.Capacity, WindowSeconds: np.WindowSeconds, RefillRate: np.RefillRate}, nil
}

func (s *stubPolicies) Get(_ context.Context, id string) (domain.Policy, error) {
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{ID: id}, nil
}

func (s *stubPolicies) List(context.Context, domain.Page) ([]domain.Policy, error) {
	return nil, s.err
}

func (s *stubPolicies) Update(_ context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error) {
	s.updated = u
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{ID: id}, nil
}

type stubRules struct {
	created  domain.NewEndpointRule
	maxRules int
	calls    int
	err      error
}

func (s *stubRules) Create(_ context.Context, policyID string, nr domain.NewEndpointRule, maxRules int) (domain.EndpointRule, error) {
	s.calls++
	s.created, s.maxRules = nr, maxRules
	if s.err != nil {
		return domain.EndpointRule{}, s.err
	}
	return domain.EndpointRule{ID: "r1", PolicyID: policyID, Method: nr.Method, PathPattern: nr.PathPattern}, nil
}

func (s *stubRules) List(context.Context, string) ([]domain.EndpointRule, error) {
	return nil, s.err
}

func (s *stubRules) Delete(context.Context, string, string) error { return s.err }

type stubAssigner struct{ err error }

func (s stubAssigner) AssignPolicy(_ context.Context, clientID, policyID string) (domain.Client, error) {
	if s.err != nil {
		return domain.Client{}, s.err
	}
	return domain.Client{ID: clientID, PolicyID: policyID}, nil
}

func newService() (*policy.Service, *stubPolicies, *stubRules, *recordingNotifier) {
	ps, rs, n := &stubPolicies{}, &stubRules{}, &recordingNotifier{}
	return policy.New(ps, rs, stubAssigner{}, n), ps, rs, n
}

func TestServiceCreateDefaultsTheRefillRate(t *testing.T) {
	svc, ps, _, n := newService()

	if _, err := svc.Create(t.Context(), domain.NewPolicy{Name: "Free", Capacity: 100, WindowSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if ps.created.RefillRate != 100.0/60 {
		t.Errorf("refill rate = %v, want 100/60", ps.created.RefillRate)
	}
	if _, err := svc.Create(t.Context(), domain.NewPolicy{Name: "Bursty", Capacity: 100, WindowSeconds: 60, RefillRate: 0.5}); err != nil {
		t.Fatal(err)
	}
	if ps.created.RefillRate != 0.5 {
		t.Errorf("explicit refill rate = %v, want 0.5 kept", ps.created.RefillRate)
	}
	if got := n.got(); len(got) != 2 || got[0] != policy.ForPolicy(freeID) {
		t.Errorf("notices = %v, want one per created policy", got)
	}
}

func TestServiceDisableSetsEnabledFalse(t *testing.T) {
	svc, ps, _, n := newService()
	if _, err := svc.Disable(t.Context(), premiumID); err != nil {
		t.Fatal(err)
	}
	if ps.updated.Enabled == nil || *ps.updated.Enabled || ps.updated.Name != nil || ps.updated.Default != nil {
		t.Errorf("update = %+v, want only Enabled = false", ps.updated)
	}
	if got := n.got(); len(got) != 1 || got[0] != policy.ForPolicy(premiumID) {
		t.Errorf("notices = %v", got)
	}
}

func TestServiceCreateRuleNormalisesAndValidates(t *testing.T) {
	tests := []struct {
		method, pattern string
		wantMethod      string
		wantField       string // "" when valid
	}{
		{"post", "/api/login", http.MethodPost, ""},
		{"*", "/api/**", "*", ""},
		{"Get", "/", http.MethodGet, ""},
		{"", "/api", "", "method"},
		{"GET POST", "/api", "", "method"},
		{"G3T", "/api", "", "method"},
		{"**", "/api", "", "method"},
		{strings.Repeat("A", 17), "/api", "", "method"},
		{http.MethodGet, "/api/", "", "path_pattern"},
		{http.MethodGet, "api", "", "path_pattern"},
		{http.MethodGet, "/a*", "", "path_pattern"},
	}
	for _, tt := range tests {
		svc, _, rs, n := newService()
		_, err := svc.CreateRule(t.Context(), freeID, domain.NewEndpointRule{Method: tt.method, PathPattern: tt.pattern, Capacity: 5, WindowSeconds: 60})
		if tt.wantField == "" {
			if err != nil {
				t.Errorf("%q %q: %v", tt.method, tt.pattern, err)
				continue
			}
			if rs.created.Method != tt.wantMethod || rs.maxRules != domain.MaxEndpointRules {
				t.Errorf("%q %q: stored method %q with limit %d, want %q and %d", tt.method, tt.pattern, rs.created.Method, rs.maxRules, tt.wantMethod, domain.MaxEndpointRules)
			}
			if got := n.got(); len(got) != 1 || got[0] != policy.ForPolicy(freeID) {
				t.Errorf("%q %q: notices = %v", tt.method, tt.pattern, got)
			}
			continue
		}
		var ve *domain.ValidationError
		if !errors.As(err, &ve) || ve.Field != tt.wantField {
			t.Errorf("%q %q: error = %v, want a ValidationError on %s", tt.method, tt.pattern, err, tt.wantField)
		}
		if rs.calls != 0 || len(n.got()) != 0 {
			t.Errorf("%q %q: an invalid rule reached the store or was announced", tt.method, tt.pattern)
		}
	}
}

func TestServiceAnnouncesEveryWrite(t *testing.T) {
	svc, _, _, n := newService()
	ctx := t.Context()

	if _, err := svc.Update(ctx, premiumID, domain.PolicyUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRule(ctx, enterpriseID, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AssignPolicy(ctx, customerApp, premiumID); err != nil {
		t.Fatal(err)
	}
	want := []policy.Change{policy.ForPolicy(premiumID), policy.ForPolicy(enterpriseID), policy.ForClient(customerApp)}
	if got := n.got(); !slices.Equal(got, want) {
		t.Errorf("notices = %v, want %v", got, want)
	}
}

func TestServiceAnnouncesNothingWhenAWriteFails(t *testing.T) {
	failure := domain.ErrPolicyNotFound
	ps, rs, n := &stubPolicies{err: failure}, &stubRules{err: failure}, &recordingNotifier{}
	svc := policy.New(ps, rs, stubAssigner{err: domain.ErrPolicyDisabled}, n)
	ctx := t.Context()

	_, errCreate := svc.Create(ctx, domain.NewPolicy{Name: "x", Capacity: 1, WindowSeconds: 1})
	_, errUpdate := svc.Update(ctx, premiumID, domain.PolicyUpdate{})
	_, errDisable := svc.Disable(ctx, premiumID)
	_, errRule := svc.CreateRule(ctx, premiumID, domain.NewEndpointRule{Method: "*", PathPattern: "/", Capacity: 1, WindowSeconds: 1})
	errDelete := svc.DeleteRule(ctx, premiumID, "r1")
	_, errList := svc.ListRules(ctx, premiumID)
	_, errAssign := svc.AssignPolicy(ctx, customerApp, premiumID)

	for name, err := range map[string]error{
		"create": errCreate, "update": errUpdate, "disable": errDisable, "create rule": errRule,
		"delete rule": errDelete, "list rules": errList,
	} {
		if !errors.Is(err, failure) {
			t.Errorf("%s error = %v, want the store's error", name, err)
		}
	}
	if !errors.Is(errAssign, domain.ErrPolicyDisabled) {
		t.Errorf("assign error = %v, want the store's error", errAssign)
	}
	if got := n.got(); len(got) != 0 {
		t.Errorf("failed writes were announced: %v", got)
	}
}
