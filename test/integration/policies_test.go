//go:build integration

package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func freePolicy() domain.NewPolicy {
	return domain.NewPolicy{Name: "Free", Capacity: 100, WindowSeconds: 60, RefillRate: 100.0 / 60, Enabled: true}
}

func TestPoliciesCreateAndGet(t *testing.T) {
	ctx := t.Context()
	repo := postgres.NewPolicies(migratedPool(t))
	before := time.Now()

	created, err := repo.Create(ctx, freePolicy())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !domain.IsID(created.ID) {
		t.Errorf("ID = %q, want a UUID", created.ID)
	}
	want := freePolicy()
	if created.Name != want.Name || created.Capacity != want.Capacity || created.WindowSeconds != want.WindowSeconds ||
		created.RefillRate != want.RefillRate || created.Enabled != want.Enabled {
		t.Errorf("Create returned %+v, want fields of %+v", created, want)
	}
	if created.CreatedAt.Location() != time.UTC || created.UpdatedAt.Location() != time.UTC {
		t.Errorf("timestamps are in %v and %v, want UTC", created.CreatedAt.Location(), created.UpdatedAt.Location())
	}
	if created.CreatedAt.Before(before.Add(-time.Minute)) || created.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("CreatedAt = %v, want about now", created.CreatedAt)
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) || got.ID != created.ID || got.RefillRate != created.RefillRate {
		t.Errorf("Get = %+v, want %+v", got, created)
	}
}

func TestPoliciesCreateDisabled(t *testing.T) {
	repo := postgres.NewPolicies(migratedPool(t))
	np := freePolicy()
	np.Enabled = false

	created, err := repo.Create(t.Context(), np)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Enabled {
		t.Error("Enabled = true, want false as requested")
	}
}

func TestPoliciesNameLengthCountsCharacters(t *testing.T) {
	repo := postgres.NewPolicies(migratedPool(t))
	np := freePolicy()
	np.Name = strings.Repeat("é", 100) // 200 bytes, 100 characters

	if _, err := repo.Create(t.Context(), np); err != nil {
		t.Errorf("Create with a 100-character multibyte name: %v", err)
	}
}

func TestPoliciesNameIsUnique(t *testing.T) {
	ctx := t.Context()
	repo := postgres.NewPolicies(migratedPool(t))

	if _, err := repo.Create(ctx, freePolicy()); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := repo.Create(ctx, freePolicy())
	if !errors.Is(err, domain.ErrPolicyNameTaken) {
		t.Errorf("second Create error = %v, want ErrPolicyNameTaken", err)
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second Create error = %v, want it to match ErrConflict", err)
	}
}

func TestPoliciesGetMissing(t *testing.T) {
	repo := postgres.NewPolicies(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "not-an-id", ""} {
		if _, err := repo.Get(t.Context(), id); !errors.Is(err, domain.ErrPolicyNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrPolicyNotFound", id, err)
		}
	}
}

func TestPoliciesConstraintsRejectBadRows(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*domain.NewPolicy)
		constraint string
	}{
		{"blank name", func(p *domain.NewPolicy) { p.Name = "   " }, "policies_name_not_blank"},
		{"name over 100 characters", func(p *domain.NewPolicy) { p.Name = strings.Repeat("é", 101) }, "policies_name_length"},
		{"zero capacity", func(p *domain.NewPolicy) { p.Capacity = 0 }, "policies_capacity_positive"},
		{"negative window", func(p *domain.NewPolicy) { p.WindowSeconds = -1 }, "policies_window_seconds_positive"},
		{"zero refill rate", func(p *domain.NewPolicy) { p.RefillRate = 0 }, "policies_refill_rate_finite_positive"},
	}
	repo := postgres.NewPolicies(migratedPool(t))
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			np := freePolicy()
			np.Name = fmt.Sprintf("policy %d", i)
			tt.mutate(&np)
			if _, err := repo.Create(t.Context(), np); !isCheckViolation(err, tt.constraint) {
				t.Errorf("Create error = %v, want check violation of %s", err, tt.constraint)
			}
		})
	}
}

func TestPoliciesListPages(t *testing.T) {
	ctx := t.Context()
	repo := postgres.NewPolicies(migratedPool(t))

	var ids []string
	for _, name := range []string{"Free", "Premium", "Enterprise"} {
		np := freePolicy()
		np.Name = name
		p, err := repo.Create(ctx, np)
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		ids = append(ids, p.ID)
	}

	first, err := repo.List(ctx, domain.Page{Limit: 2})
	if err != nil {
		t.Fatalf("List first page: %v", err)
	}
	if len(first) != 2 || first[0].ID != ids[0] || first[1].ID != ids[1] {
		t.Fatalf("first page = %v, want the first two created", policyIDs(first))
	}

	second, err := repo.List(ctx, domain.Page{After: first[1].ID, Limit: 2})
	if err != nil {
		t.Fatalf("List second page: %v", err)
	}
	if len(second) != 1 || second[0].ID != ids[2] {
		t.Fatalf("second page = %v, want only the third created", policyIDs(second))
	}

	empty, err := repo.List(ctx, domain.Page{After: second[0].ID, Limit: 2})
	if err != nil {
		t.Fatalf("List past the end: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("past the end = %v, want none", policyIDs(empty))
	}
}

func TestPoliciesListRejectsBadPage(t *testing.T) {
	repo := postgres.NewPolicies(migratedPool(t))
	for _, page := range []domain.Page{{Limit: 0}, {Limit: domain.MaxPageLimit + 1}, {After: "nope", Limit: 10}} {
		if _, err := repo.List(t.Context(), page); err == nil {
			t.Errorf("List(%+v): want error, got nil", page)
		}
	}
}

func policyIDs(ps []domain.Policy) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}
