//go:build integration

package integration

import (
	"errors"
	"sync"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func ptr[T any](v T) *T { return &v }

func TestPoliciesUpdateFields(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewPolicies(pool)
	p := createPolicy(t, pool, freePolicy())

	tests := []struct {
		name   string
		update domain.PolicyUpdate
		check  func(t *testing.T, got domain.Policy)
	}{
		{
			name:   "nothing given changes only updated_at",
			update: domain.PolicyUpdate{},
			check: func(t *testing.T, got domain.Policy) {
				if got.Name != "Free" || got.Capacity != 100 || got.WindowSeconds != 60 || got.RefillRate != 100.0/60 || !got.Enabled {
					t.Errorf("got %+v, want the policy unchanged", got)
				}
			},
		},
		{
			name:   "rename",
			update: domain.PolicyUpdate{Name: ptr("Free tier")},
			check: func(t *testing.T, got domain.Policy) {
				if got.Name != "Free tier" || got.Capacity != 100 {
					t.Errorf("got %+v", got)
				}
			},
		},
		{
			name:   "capacity alone recomputes the refill rate over the current window",
			update: domain.PolicyUpdate{Capacity: ptr(120)},
			check: func(t *testing.T, got domain.Policy) {
				if got.Capacity != 120 || got.WindowSeconds != 60 || got.RefillRate != 2 {
					t.Errorf("got capacity %d window %d rate %v, want 120, 60, 2", got.Capacity, got.WindowSeconds, got.RefillRate)
				}
			},
		},
		{
			name:   "window alone recomputes the refill rate over the current capacity",
			update: domain.PolicyUpdate{WindowSeconds: ptr(30)},
			check: func(t *testing.T, got domain.Policy) {
				if got.Capacity != 120 || got.WindowSeconds != 30 || got.RefillRate != 4 {
					t.Errorf("got capacity %d window %d rate %v, want 120, 30, 4", got.Capacity, got.WindowSeconds, got.RefillRate)
				}
			},
		},
		{
			name:   "an explicit refill rate wins over the recomputation",
			update: domain.PolicyUpdate{Capacity: ptr(600), RefillRate: ptr(1.5)},
			check: func(t *testing.T, got domain.Policy) {
				if got.Capacity != 600 || got.RefillRate != 1.5 {
					t.Errorf("got capacity %d rate %v, want 600 and 1.5", got.Capacity, got.RefillRate)
				}
			},
		},
		{
			name:   "an unrelated change keeps an explicit refill rate",
			update: domain.PolicyUpdate{Name: ptr("Free")},
			check: func(t *testing.T, got domain.Policy) {
				if got.RefillRate != 1.5 {
					t.Errorf("RefillRate = %v, want 1.5 kept", got.RefillRate)
				}
			},
		},
		{
			name:   "disable",
			update: domain.PolicyUpdate{Enabled: ptr(false)},
			check: func(t *testing.T, got domain.Policy) {
				if got.Enabled {
					t.Error("Enabled = true, want false")
				}
			},
		},
	}

	last := p.UpdatedAt
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.Update(ctx, p.ID, tt.update)
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			tt.check(t, got)
			if got.UpdatedAt.Before(last) {
				t.Errorf("UpdatedAt went backwards: %v after %v", got.UpdatedAt, last)
			}
			if !got.CreatedAt.Equal(p.CreatedAt) {
				t.Errorf("CreatedAt changed: %v, want %v", got.CreatedAt, p.CreatedAt)
			}
			last = got.UpdatedAt
			stored, err := repo.Get(ctx, p.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if stored != got {
				t.Errorf("stored %+v, Update returned %+v", stored, got)
			}
		})
	}
}

func TestPoliciesUpdateErrors(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewPolicies(pool)
	createPolicy(t, pool, freePolicy())
	premium := freePolicy()
	premium.Name = "Premium"
	p := createPolicy(t, pool, premium)

	if _, err := repo.Update(ctx, p.ID, domain.PolicyUpdate{Name: ptr("Free")}); !errors.Is(err, domain.ErrPolicyNameTaken) {
		t.Errorf("renaming onto a taken name: error = %v, want ErrPolicyNameTaken", err)
	}
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, err := repo.Update(ctx, id, domain.PolicyUpdate{Default: ptr(true)}); !errors.Is(err, domain.ErrPolicyNotFound) {
			t.Errorf("Update(%q): error = %v, want ErrPolicyNotFound", id, err)
		}
	}
	if _, err := repo.Update(ctx, p.ID, domain.PolicyUpdate{Capacity: ptr(0)}); !isCheckViolation(err, "policies_capacity_positive") {
		t.Errorf("zero capacity: error = %v, want the check violation", err)
	}
}

func TestPoliciesDefaultMovesBetweenPolicies(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewPolicies(pool)

	if id, err := repo.DefaultID(ctx); err != nil || id != "" {
		t.Fatalf("DefaultID with no default = %q, %v; want empty", id, err)
	}

	np := freePolicy()
	np.Default = true
	free := createPolicy(t, pool, np)
	if !free.Default {
		t.Fatal("created with Default = true, but the row says false")
	}
	expectDefault(t, repo, free.ID)

	// A new default policy takes the flag.
	premium := freePolicy()
	premium.Name, premium.Default = "Premium", true
	p := createPolicy(t, pool, premium)
	expectDefault(t, repo, p.ID)
	if got, _ := repo.Get(ctx, free.ID); got.Default {
		t.Error("the previous default kept its flag")
	}

	// An update takes it back.
	if _, err := repo.Update(ctx, free.ID, domain.PolicyUpdate{Default: ptr(true)}); err != nil {
		t.Fatalf("Update to default: %v", err)
	}
	expectDefault(t, repo, free.ID)

	// Making the default the default again changes nothing.
	if _, err := repo.Update(ctx, free.ID, domain.PolicyUpdate{Default: ptr(true)}); err != nil {
		t.Fatalf("Update to default again: %v", err)
	}
	expectDefault(t, repo, free.ID)

	// Clearing it leaves no default.
	if _, err := repo.Update(ctx, free.ID, domain.PolicyUpdate{Default: ptr(false)}); err != nil {
		t.Fatalf("Update to not default: %v", err)
	}
	expectDefault(t, repo, "")
}

func TestPoliciesDefaultHoldsUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewPolicies(pool)

	var ids []string
	for _, name := range []string{"A", "B", "C", "D", "E", "F"} {
		np := freePolicy()
		np.Name = name
		ids = append(ids, createPolicy(t, pool, np).ID)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for range 5 {
		for _, id := range ids {
			wg.Go(func() {
				if _, err := repo.Update(ctx, id, domain.PolicyUpdate{Default: ptr(true)}); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent default changes failed: %v", errs)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM policies WHERE is_default`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d default policies after concurrent changes, want exactly 1", n)
	}
}

func TestPoliciesGetWithRules(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewPolicies(pool)
	p := createPolicy(t, pool, freePolicy())

	got, rules, err := repo.GetWithRules(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetWithRules with no rules: %v", err)
	}
	if got != p || len(rules) != 0 {
		t.Errorf("GetWithRules = %+v, %v; want %+v and no rules", got, rules, p)
	}

	r, err := postgres.NewEndpointRules(pool).Create(ctx, p.ID, loginRule(), domain.MaxEndpointRules)
	if err != nil {
		t.Fatalf("creating rule: %v", err)
	}
	_, rules, err = repo.GetWithRules(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetWithRules: %v", err)
	}
	if len(rules) != 1 || rules[0] != r {
		t.Errorf("rules = %+v, want [%+v]", rules, r)
	}

	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, _, err := repo.GetWithRules(ctx, id); !errors.Is(err, domain.ErrPolicyNotFound) {
			t.Errorf("GetWithRules(%q): error = %v, want ErrPolicyNotFound", id, err)
		}
	}
}

func expectDefault(t *testing.T, repo *postgres.Policies, want string) {
	t.Helper()
	got, err := repo.DefaultID(t.Context())
	if err != nil {
		t.Fatalf("DefaultID: %v", err)
	}
	if got != want {
		t.Fatalf("DefaultID = %q, want %q", got, want)
	}
}
