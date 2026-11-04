//go:build integration

package integration

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func loginRule() domain.NewEndpointRule {
	return domain.NewEndpointRule{Method: http.MethodPost, PathPattern: "/api/login", Capacity: 5, WindowSeconds: 60}
}

func createPolicy(t *testing.T, pool *pgxpool.Pool, np domain.NewPolicy) domain.Policy {
	t.Helper()
	p, err := postgres.NewPolicies(pool).Create(t.Context(), np)
	if err != nil {
		t.Fatalf("creating policy %s: %v", np.Name, err)
	}
	return p
}

func TestRulesCreateAndList(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	policy := createPolicy(t, pool, freePolicy())
	rules := postgres.NewEndpointRules(pool)

	login, err := rules.Create(ctx, policy.ID, loginRule(), domain.MaxEndpointRules)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !domain.IsID(login.ID) || login.PolicyID != policy.ID || login.Method != http.MethodPost ||
		login.PathPattern != "/api/login" || login.Capacity != 5 || login.WindowSeconds != 60 {
		t.Errorf("Create returned %+v", login)
	}
	if login.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt is in %v, want UTC", login.CreatedAt.Location())
	}

	orders, err := rules.Create(ctx, policy.ID, domain.NewEndpointRule{Method: "*", PathPattern: "/api/orders/**", Capacity: 20, WindowSeconds: 60}, domain.MaxEndpointRules)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	got, err := rules.List(ctx, policy.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != login.ID || got[1].ID != orders.ID {
		t.Errorf("List = %+v, want the two rules in creation order", got)
	}
}

func TestRulesSamePatternTwiceConflicts(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	policy := createPolicy(t, pool, freePolicy())
	rules := postgres.NewEndpointRules(pool)

	if _, err := rules.Create(ctx, policy.ID, loginRule(), domain.MaxEndpointRules); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := rules.Create(ctx, policy.ID, loginRule(), domain.MaxEndpointRules)
	if !errors.Is(err, domain.ErrEndpointRuleExists) {
		t.Errorf("duplicate Create error = %v, want ErrEndpointRuleExists", err)
	}

	// The same pattern with another method, or in another policy, is fine.
	other := loginRule()
	other.Method = "*"
	if _, err := rules.Create(ctx, policy.ID, other, domain.MaxEndpointRules); err != nil {
		t.Errorf("same pattern for any method: %v", err)
	}
	premium := freePolicy()
	premium.Name = "Premium"
	p2 := createPolicy(t, pool, premium)
	if _, err := rules.Create(ctx, p2.ID, loginRule(), domain.MaxEndpointRules); err != nil {
		t.Errorf("same rule in another policy: %v", err)
	}
}

func TestRulesLimitHoldsUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	policy := createPolicy(t, pool, freePolicy())
	rules := postgres.NewEndpointRules(pool)
	const limit, attempts = 3, 12

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		limited int
		other   []error
	)
	for i := range attempts {
		wg.Go(func() {
			nr := loginRule()
			nr.PathPattern = fmt.Sprintf("/api/r%d", i)
			_, err := rules.Create(ctx, policy.ID, nr, limit)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, domain.ErrEndpointRuleLimit):
				limited++
			default:
				other = append(other, err)
			}
		})
	}
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if created != limit || limited != attempts-limit {
		t.Errorf("created %d and refused %d, want %d and %d", created, limited, limit, attempts-limit)
	}
	got, err := rules.List(ctx, policy.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != limit {
		t.Errorf("stored %d rules, want %d", len(got), limit)
	}
}

func TestRulesForMissingPolicy(t *testing.T) {
	rules := postgres.NewEndpointRules(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, err := rules.Create(t.Context(), id, loginRule(), domain.MaxEndpointRules); !errors.Is(err, domain.ErrPolicyNotFound) {
			t.Errorf("Create on policy %q: error = %v, want ErrPolicyNotFound", id, err)
		}
		got, err := rules.List(t.Context(), id)
		if err != nil || len(got) != 0 {
			t.Errorf("List on policy %q = %v, %v; want empty, nil", id, got, err)
		}
	}
}

func TestRulesDeleteIsScopedToThePolicy(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	free := createPolicy(t, pool, freePolicy())
	premium := freePolicy()
	premium.Name = "Premium"
	other := createPolicy(t, pool, premium)
	rules := postgres.NewEndpointRules(pool)

	r, err := rules.Create(ctx, free.ID, loginRule(), domain.MaxEndpointRules)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := rules.Delete(ctx, other.ID, r.ID); !errors.Is(err, domain.ErrEndpointRuleNotFound) {
		t.Errorf("Delete through another policy: error = %v, want ErrEndpointRuleNotFound", err)
	}
	if err := rules.Delete(ctx, free.ID, "garbage"); !errors.Is(err, domain.ErrEndpointRuleNotFound) {
		t.Errorf("Delete of a malformed ID: error = %v, want ErrEndpointRuleNotFound", err)
	}
	if err := rules.Delete(ctx, free.ID, r.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := rules.Delete(ctx, free.ID, r.ID); !errors.Is(err, domain.ErrEndpointRuleNotFound) {
		t.Errorf("second Delete: error = %v, want ErrEndpointRuleNotFound", err)
	}
	if got, err := rules.List(ctx, free.ID); err != nil || len(got) != 0 {
		t.Errorf("List after Delete = %v, %v; want empty", got, err)
	}
}

func TestRulesConstraintsRejectBadRows(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*domain.NewEndpointRule)
		constraint string
	}{
		{"lower-case method", func(r *domain.NewEndpointRule) { r.Method = "post" }, "endpoint_rules_method_valid"},
		{"empty method", func(r *domain.NewEndpointRule) { r.Method = "" }, "endpoint_rules_method_valid"},
		{"relative pattern", func(r *domain.NewEndpointRule) { r.PathPattern = "api/login" }, "endpoint_rules_path_pattern_shape"},
		{"pattern over 512 bytes", func(r *domain.NewEndpointRule) { r.PathPattern = "/" + strings.Repeat("a", 512) }, "endpoint_rules_path_pattern_shape"},
		{"zero capacity", func(r *domain.NewEndpointRule) { r.Capacity = 0 }, "endpoint_rules_capacity_positive"},
		{"zero window", func(r *domain.NewEndpointRule) { r.WindowSeconds = 0 }, "endpoint_rules_window_seconds_positive"},
	}
	pool := migratedPool(t)
	policy := createPolicy(t, pool, freePolicy())
	rules := postgres.NewEndpointRules(pool)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nr := loginRule()
			tt.mutate(&nr)
			if _, err := rules.Create(t.Context(), policy.ID, nr, domain.MaxEndpointRules); !isCheckViolation(err, tt.constraint) {
				t.Errorf("Create error = %v, want check violation of %s", err, tt.constraint)
			}
		})
	}
}
