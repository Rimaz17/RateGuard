package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const ruleColumns = `id, policy_id, method, path_pattern, capacity, window_seconds, created_at`

// EndpointRules stores policies' endpoint rules.
type EndpointRules struct {
	pool *pgxpool.Pool
}

// NewEndpointRules returns an endpoint rule store on pool.
func NewEndpointRules(pool *pgxpool.Pool) *EndpointRules {
	return &EndpointRules{pool: pool}
}

// Create adds a rule to the policy unless it already holds maxRules rules
// (domain.ErrEndpointRuleLimit) or a rule for the same method and pattern
// (domain.ErrEndpointRuleExists). The policy row is locked for the
// duration, so concurrent creates for one policy queue up and the limit
// holds under any interleaving. The rule's method and pattern must already
// be validated.
func (r *EndpointRules) Create(ctx context.Context, policyID string, nr domain.NewEndpointRule, maxRules int) (domain.EndpointRule, error) {
	if !domain.IsID(policyID) {
		return domain.EndpointRule{}, domain.ErrPolicyNotFound
	}
	var created domain.EndpointRule
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// NO KEY UPDATE is enough to serialise rule creation, and unlike
		// FOR UPDATE it does not block clients being assigned the policy.
		var locked string
		err := tx.QueryRow(ctx, `SELECT id FROM policies WHERE id = $1 FOR NO KEY UPDATE`, policyID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrPolicyNotFound
		}
		if err != nil {
			return fmt.Errorf("locking policy: %w", err)
		}

		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM endpoint_rules WHERE policy_id = $1`, policyID).Scan(&n); err != nil {
			return fmt.Errorf("counting rules: %w", err)
		}
		if n >= maxRules {
			return domain.ErrEndpointRuleLimit
		}

		created, err = scanRule(tx.QueryRow(ctx, `
			INSERT INTO endpoint_rules (policy_id, method, path_pattern, capacity, window_seconds)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING `+ruleColumns,
			policyID, nr.Method, nr.PathPattern, nr.Capacity, nr.WindowSeconds))
		if isUniqueViolation(err, "endpoint_rules_policy_method_pattern_key") {
			return domain.ErrEndpointRuleExists
		}
		return err
	})
	if err != nil {
		return domain.EndpointRule{}, fmt.Errorf("creating endpoint rule: %w", err)
	}
	return created, nil
}

// List returns the policy's rules in creation order. A policy with no
// rules, or no such policy, yields an empty list.
func (r *EndpointRules) List(ctx context.Context, policyID string) ([]domain.EndpointRule, error) {
	if !domain.IsID(policyID) {
		return nil, nil
	}
	return listRules(ctx, r.pool, policyID)
}

// Delete removes one of the policy's rules. A rule that does not exist or
// belongs to another policy returns domain.ErrEndpointRuleNotFound.
func (r *EndpointRules) Delete(ctx context.Context, policyID, ruleID string) error {
	if !domain.IsID(policyID) || !domain.IsID(ruleID) {
		return domain.ErrEndpointRuleNotFound
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM endpoint_rules WHERE id = $1 AND policy_id = $2`, ruleID, policyID)
	if err != nil {
		return fmt.Errorf("deleting endpoint rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrEndpointRuleNotFound
	}
	return nil
}

// querier is what listRules needs: a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listRules(ctx context.Context, q querier, policyID string) ([]domain.EndpointRule, error) {
	rows, err := q.Query(ctx, `SELECT `+ruleColumns+` FROM endpoint_rules WHERE policy_id = $1 ORDER BY id`, policyID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoint rules: %w", err)
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.EndpointRule, error) {
		return scanRule(row)
	})
	if err != nil {
		return nil, fmt.Errorf("listing endpoint rules: %w", err)
	}
	return rules, nil
}

func scanRule(row pgx.Row) (domain.EndpointRule, error) {
	var r domain.EndpointRule
	err := row.Scan(&r.ID, &r.PolicyID, &r.Method, &r.PathPattern, &r.Capacity, &r.WindowSeconds, &r.CreatedAt)
	r.CreatedAt = r.CreatedAt.UTC()
	return r, err
}
