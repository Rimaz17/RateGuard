//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func TestAssignPolicy(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	clients := postgres.NewClients(pool)
	free := createPolicy(t, pool, freePolicy())
	premium := freePolicy()
	premium.Name = "Premium"
	prem := createPolicy(t, pool, premium)

	c, err := clients.Create(ctx, "Customer App")
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	if id, err := clients.PolicyID(ctx, c.ID); err != nil || id != "" {
		t.Fatalf("PolicyID before assignment = %q, %v; want empty", id, err)
	}

	for _, want := range []string{free.ID, prem.ID, ""} {
		got, err := clients.AssignPolicy(ctx, c.ID, want)
		if err != nil {
			t.Fatalf("AssignPolicy(%q): %v", want, err)
		}
		if got.PolicyID != want || got.ID != c.ID {
			t.Errorf("AssignPolicy(%q) returned %+v", want, got)
		}
		if got.UpdatedAt.Before(c.UpdatedAt) {
			t.Errorf("UpdatedAt = %v, want no earlier than %v", got.UpdatedAt, c.UpdatedAt)
		}
		if id, err := clients.PolicyID(ctx, c.ID); err != nil || id != want {
			t.Errorf("PolicyID after assigning %q = %q, %v", want, id, err)
		}
	}
}

func TestAssignPolicyErrors(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	clients := postgres.NewClients(pool)
	policies := postgres.NewPolicies(pool)
	free := createPolicy(t, pool, freePolicy())
	c, err := clients.Create(ctx, "Customer App")
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	if _, err := clients.AssignPolicy(ctx, c.ID, free.ID); err != nil {
		t.Fatalf("AssignPolicy: %v", err)
	}
	disabled := freePolicy()
	disabled.Name, disabled.Enabled = "Retired", false
	retired := createPolicy(t, pool, disabled)

	const missing = "01a1a2fb-68ca-772e-a1e6-072972830b19"
	tests := []struct {
		name               string
		clientID, policyID string
		want               error
	}{
		{"unknown client", missing, free.ID, domain.ErrClientNotFound},
		{"malformed client", "nope", free.ID, domain.ErrClientNotFound},
		{"unknown client and policy reports the client", missing, missing, domain.ErrClientNotFound},
		{"unknown policy", c.ID, missing, domain.ErrPolicyNotFound},
		{"malformed policy", c.ID, "nope", domain.ErrPolicyNotFound},
		{"disabled policy", c.ID, retired.ID, domain.ErrPolicyDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := clients.AssignPolicy(ctx, tt.clientID, tt.policyID); !errors.Is(err, tt.want) {
				t.Errorf("AssignPolicy error = %v, want %v", err, tt.want)
			}
		})
	}

	// A refused assignment leaves the previous one in place.
	if id, err := clients.PolicyID(ctx, c.ID); err != nil || id != free.ID {
		t.Errorf("PolicyID after refused assignments = %q, %v; want %q", id, err, free.ID)
	}

	// Disabling a policy after assignment is allowed: resolution falls back
	// to the default (ADR 0009), and the assignment stays.
	if _, err := policies.Update(ctx, free.ID, domain.PolicyUpdate{Enabled: ptr(false)}); err != nil {
		t.Fatalf("disabling an assigned policy: %v", err)
	}
	if id, err := clients.PolicyID(ctx, c.ID); err != nil || id != free.ID {
		t.Errorf("PolicyID after disabling its policy = %q, %v; want %q", id, err, free.ID)
	}
}

func TestPolicyIDOfMissingClient(t *testing.T) {
	clients := postgres.NewClients(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, err := clients.PolicyID(t.Context(), id); !errors.Is(err, domain.ErrClientNotFound) {
			t.Errorf("PolicyID(%q): error = %v, want ErrClientNotFound", id, err)
		}
	}
}
