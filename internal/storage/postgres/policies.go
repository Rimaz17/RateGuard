package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const policyColumns = `id, name, capacity, window_seconds, refill_rate, enabled, created_at, updated_at`

// Policies stores rate-limit policies.
type Policies struct {
	pool *pgxpool.Pool
}

// NewPolicies returns a policy store on pool.
func NewPolicies(pool *pgxpool.Pool) *Policies {
	return &Policies{pool: pool}
}

// Create inserts a policy. A name already in use returns
// domain.ErrPolicyNameTaken.
func (r *Policies) Create(ctx context.Context, np domain.NewPolicy) (domain.Policy, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO policies (name, capacity, window_seconds, refill_rate, enabled)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+policyColumns,
		np.Name, np.Capacity, np.WindowSeconds, np.RefillRate, np.Enabled)
	p, err := scanPolicy(row)
	if isUniqueViolation(err, "policies_name_key") {
		return domain.Policy{}, domain.ErrPolicyNameTaken
	}
	if err != nil {
		return domain.Policy{}, fmt.Errorf("inserting policy: %w", err)
	}
	return p, nil
}

// Get returns the policy with the given ID, or domain.ErrPolicyNotFound.
func (r *Policies) Get(ctx context.Context, id string) (domain.Policy, error) {
	if !domain.IsID(id) {
		return domain.Policy{}, domain.ErrPolicyNotFound
	}
	row := r.pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies WHERE id = $1`, id)
	p, err := scanPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Policy{}, domain.ErrPolicyNotFound
	}
	if err != nil {
		return domain.Policy{}, fmt.Errorf("selecting policy: %w", err)
	}
	return p, nil
}

// List returns one page of policies in creation order.
func (r *Policies) List(ctx context.Context, page domain.Page) ([]domain.Policy, error) {
	after, err := afterParam(page)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+policyColumns+` FROM policies
		WHERE $1::uuid IS NULL OR id > $1::uuid
		ORDER BY id
		LIMIT $2`,
		after, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing policies: %w", err)
	}
	policies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Policy, error) {
		return scanPolicy(row)
	})
	if err != nil {
		return nil, fmt.Errorf("listing policies: %w", err)
	}
	return policies, nil
}

func scanPolicy(row pgx.Row) (domain.Policy, error) {
	var p domain.Policy
	err := row.Scan(&p.ID, &p.Name, &p.Capacity, &p.WindowSeconds, &p.RefillRate, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, err
}
