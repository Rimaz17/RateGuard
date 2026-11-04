//go:build integration

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func TestClientsCreateAndGet(t *testing.T) {
	ctx := t.Context()
	repo := postgres.NewClients(migratedPool(t))

	created, err := repo.Create(ctx, "Customer App")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !domain.IsID(created.ID) {
		t.Errorf("ID = %q, want a UUID", created.ID)
	}
	if created.Name != "Customer App" {
		t.Errorf("Name = %q, want %q", created.Name, "Customer App")
	}
	if created.Status != domain.ClientActive {
		t.Errorf("Status = %q, want %q", created.Status, domain.ClientActive)
	}
	if created.PolicyID != "" {
		t.Errorf("PolicyID = %q, want none assigned", created.PolicyID)
	}
	if created.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt is in %v, want UTC", created.CreatedAt.Location())
	}

	got, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.Status != created.Status || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("Get = %+v, want %+v", got, created)
	}
}

func TestClientsNamesNeedNotBeUnique(t *testing.T) {
	repo := postgres.NewClients(migratedPool(t))
	a, err := repo.Create(t.Context(), "Mobile")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	b, err := repo.Create(t.Context(), "Mobile")
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if a.ID == b.ID {
		t.Error("two clients got the same ID")
	}
}

func TestClientsGetMissing(t *testing.T) {
	repo := postgres.NewClients(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "not-an-id", ""} {
		if _, err := repo.Get(t.Context(), id); !errors.Is(err, domain.ErrClientNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrClientNotFound", id, err)
		}
	}
}

func TestClientsConstraintsRejectBadRows(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	repo := postgres.NewClients(pool)

	_, err := repo.Create(ctx, " ")
	if !isCheckViolation(err, "clients_name_not_blank") {
		t.Errorf("Create with a blank name: error = %v, want clients_name_not_blank", err)
	}

	c, err := repo.Create(ctx, "Merchant App")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE clients SET status = 'suspended' WHERE id = $1`, c.ID)
	if !isCheckViolation(err, "clients_status_valid") {
		t.Errorf("setting an unknown status: error = %v, want clients_status_valid", err)
	}
	_, err = pool.Exec(ctx, `UPDATE clients SET policy_id = '01a1a2fb-68ca-772e-a1e6-072972830b19' WHERE id = $1`, c.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "clients_policy_id_fkey" {
		t.Errorf("assigning a missing policy: error = %v, want a foreign key violation", err)
	}
}

func TestClientsReadAssignedPolicy(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	clients := postgres.NewClients(pool)

	policy, err := postgres.NewPolicies(pool).Create(ctx, freePolicy())
	if err != nil {
		t.Fatalf("creating policy: %v", err)
	}
	c, err := clients.Create(ctx, "Admin App")
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	if _, err := clients.AssignPolicy(ctx, c.ID, policy.ID); err != nil {
		t.Fatalf("assigning policy: %v", err)
	}

	got, err := clients.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PolicyID != policy.ID {
		t.Errorf("PolicyID = %q, want %q", got.PolicyID, policy.ID)
	}
}

func TestClientsListPages(t *testing.T) {
	ctx := t.Context()
	repo := postgres.NewClients(migratedPool(t))

	var ids []string
	for _, name := range []string{"Customer App", "Merchant App", "Admin App"} {
		c, err := repo.Create(ctx, name)
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		ids = append(ids, c.ID)
	}

	all, err := repo.List(ctx, domain.Page{Limit: domain.MaxPageLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := clientIDs(all); len(got) != 3 || got[0] != ids[0] || got[1] != ids[1] || got[2] != ids[2] {
		t.Fatalf("List = %v, want %v in creation order", got, ids)
	}

	page, err := repo.List(ctx, domain.Page{After: ids[0], Limit: 1})
	if err != nil {
		t.Fatalf("List after first: %v", err)
	}
	if got := clientIDs(page); len(got) != 1 || got[0] != ids[1] {
		t.Errorf("List after first = %v, want [%s]", got, ids[1])
	}
}

func isCheckViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == constraint
}

func clientIDs(cs []domain.Client) []string {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.ID
	}
	return ids
}
