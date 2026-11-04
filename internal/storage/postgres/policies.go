package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const policyColumns = `id, name, capacity, window_seconds, refill_rate, enabled, is_default, created_at, updated_at`

// defaultPolicyLock is the advisory lock key that serialises changes to
// which policy is the default. Without it, two transactions making
// different policies the default would each clear the old flag, and the
// later one would fail on policies_one_default_idx instead of winning.
const defaultPolicyLock int64 = 0x52474450 // "RGDP"

// Policies stores rate-limit policies.
type Policies struct {
	pool *pgxpool.Pool
}

// NewPolicies returns a policy store on pool.
func NewPolicies(pool *pgxpool.Pool) *Policies {
	return &Policies{pool: pool}
}

// Create inserts a policy. A name already in use returns
// domain.ErrPolicyNameTaken. A new default policy takes the flag from the
// previous one in the same transaction.
func (r *Policies) Create(ctx context.Context, np domain.NewPolicy) (domain.Policy, error) {
	var p domain.Policy
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if np.Default {
			if err := clearDefault(ctx, tx, ""); err != nil {
				return err
			}
		}
		var err error
		p, err = scanPolicy(tx.QueryRow(ctx, `
			INSERT INTO policies (name, capacity, window_seconds, refill_rate, enabled, is_default)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING `+policyColumns,
			np.Name, np.Capacity, np.WindowSeconds, np.RefillRate, np.Enabled, np.Default))
		return err
	})
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

// Update applies the non-nil fields of u and returns the updated policy, or
// domain.ErrPolicyNotFound or domain.ErrPolicyNameTaken. When capacity or
// window changes without a refill rate, the refill rate is recomputed as
// capacity / window_seconds (ADR 0009). The arithmetic is done in SQL on the
// row being updated, so a concurrent update cannot interleave with it.
func (r *Policies) Update(ctx context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error) {
	if !domain.IsID(id) {
		return domain.Policy{}, domain.ErrPolicyNotFound
	}
	var p domain.Policy
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if u.Default != nil && *u.Default {
			if err := clearDefault(ctx, tx, id); err != nil {
				return err
			}
		}
		// In SET, every column reference reads the row as it was before
		// this UPDATE, so refill_rate sees the old capacity and window
		// unless new ones are given.
		var err error
		p, err = scanPolicy(tx.QueryRow(ctx, `
			UPDATE policies SET
				name           = COALESCE($2, name),
				capacity       = COALESCE($3, capacity),
				window_seconds = COALESCE($4, window_seconds),
				refill_rate    = CASE
					WHEN $5::double precision IS NOT NULL THEN $5::double precision
					WHEN $3::integer IS NOT NULL OR $4::integer IS NOT NULL
						THEN COALESCE($3::integer, capacity)::double precision / COALESCE($4::integer, window_seconds)
					ELSE refill_rate
				END,
				enabled        = COALESCE($6, enabled),
				is_default     = COALESCE($7, is_default),
				updated_at     = now()
			WHERE id = $1
			RETURNING `+policyColumns,
			id, u.Name, u.Capacity, u.WindowSeconds, u.RefillRate, u.Enabled, u.Default))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrPolicyNotFound
		}
		return err
	})
	switch {
	case errors.Is(err, domain.ErrPolicyNotFound):
		return domain.Policy{}, domain.ErrPolicyNotFound
	case isUniqueViolation(err, "policies_name_key"):
		return domain.Policy{}, domain.ErrPolicyNameTaken
	case err != nil:
		return domain.Policy{}, fmt.Errorf("updating policy: %w", err)
	}
	return p, nil
}

// GetWithRules returns a policy and its endpoint rules as one consistent
// snapshot, for the policy cache. A missing policy is
// domain.ErrPolicyNotFound.
func (r *Policies) GetWithRules(ctx context.Context, id string) (domain.Policy, []domain.EndpointRule, error) {
	if !domain.IsID(id) {
		return domain.Policy{}, nil, domain.ErrPolicyNotFound
	}
	var (
		p     domain.Policy
		rules []domain.EndpointRule
	)
	// Repeatable read gives both queries one snapshot, so a rule added
	// between them cannot appear without its policy's matching state.
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, r.pool, opts, func(tx pgx.Tx) error {
		var err error
		p, err = scanPolicy(tx.QueryRow(ctx, `SELECT `+policyColumns+` FROM policies WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrPolicyNotFound
		}
		if err != nil {
			return fmt.Errorf("selecting policy: %w", err)
		}
		rules, err = listRules(ctx, tx, id)
		return err
	})
	if errors.Is(err, domain.ErrPolicyNotFound) {
		return domain.Policy{}, nil, domain.ErrPolicyNotFound
	}
	if err != nil {
		return domain.Policy{}, nil, fmt.Errorf("loading policy with rules: %w", err)
	}
	return p, rules, nil
}

// DefaultID returns the ID of the default policy, or "" when no policy is
// the default.
func (r *Policies) DefaultID(ctx context.Context) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT id FROM policies WHERE is_default`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("selecting default policy: %w", err)
	}
	return id, nil
}

// clearDefault takes the default-policy lock for the rest of tx and clears
// the flag from every policy except keep, so the caller can set it on
// another.
func clearDefault(ctx context.Context, tx pgx.Tx, keep string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, defaultPolicyLock); err != nil {
		return fmt.Errorf("locking the default policy: %w", err)
	}
	var keepParam *string
	if keep != "" {
		keepParam = &keep
	}
	_, err := tx.Exec(ctx, `
		UPDATE policies SET is_default = FALSE, updated_at = now()
		WHERE is_default AND ($1::uuid IS NULL OR id <> $1::uuid)`,
		keepParam)
	if err != nil {
		return fmt.Errorf("clearing the default policy: %w", err)
	}
	return nil
}

func scanPolicy(row pgx.Row) (domain.Policy, error) {
	var p domain.Policy
	err := row.Scan(&p.ID, &p.Name, &p.Capacity, &p.WindowSeconds, &p.RefillRate, &p.Enabled, &p.Default, &p.CreatedAt, &p.UpdatedAt)
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()
	return p, err
}
