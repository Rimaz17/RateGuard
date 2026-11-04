package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const clientColumns = `id, name, policy_id, status, created_at, updated_at`

// Clients stores registered client applications.
type Clients struct {
	pool *pgxpool.Pool
}

// NewClients returns a client store on pool.
func NewClients(pool *pgxpool.Pool) *Clients {
	return &Clients{pool: pool}
}

// Create inserts an active client with no policy assigned.
func (r *Clients) Create(ctx context.Context, name string) (domain.Client, error) {
	row := r.pool.QueryRow(ctx, `INSERT INTO clients (name) VALUES ($1) RETURNING `+clientColumns, name)
	c, err := scanClient(row)
	if err != nil {
		return domain.Client{}, fmt.Errorf("inserting client: %w", err)
	}
	return c, nil
}

// CreateWithKey registers a client together with its first API key, in one
// transaction: there is never a client without a key to call with, nor a
// key without its client. A prefix collision returns
// domain.ErrAPIKeyPrefixTaken and leaves nothing behind.
func (r *Clients) CreateWithKey(ctx context.Context, name string, key domain.NewAPIKey) (domain.Client, domain.APIKey, error) {
	var (
		c domain.Client
		k domain.APIKey
	)
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanClient(tx.QueryRow(ctx, `INSERT INTO clients (name) VALUES ($1) RETURNING `+clientColumns, name))
		if err != nil {
			return fmt.Errorf("inserting client: %w", err)
		}
		k, err = insertKey(ctx, tx, c.ID, key)
		return err
	})
	if err != nil {
		return domain.Client{}, domain.APIKey{}, fmt.Errorf("registering client: %w", err)
	}
	return c, k, nil
}

// Get returns the client with the given ID, or domain.ErrClientNotFound.
func (r *Clients) Get(ctx context.Context, id string) (domain.Client, error) {
	if !domain.IsID(id) {
		return domain.Client{}, domain.ErrClientNotFound
	}
	row := r.pool.QueryRow(ctx, `SELECT `+clientColumns+` FROM clients WHERE id = $1`, id)
	c, err := scanClient(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Client{}, domain.ErrClientNotFound
	}
	if err != nil {
		return domain.Client{}, fmt.Errorf("selecting client: %w", err)
	}
	return c, nil
}

// List returns one page of clients in registration order.
func (r *Clients) List(ctx context.Context, page domain.Page) ([]domain.Client, error) {
	after, err := afterParam(page)
	if err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+clientColumns+` FROM clients
		WHERE $1::uuid IS NULL OR id > $1::uuid
		ORDER BY id
		LIMIT $2`,
		after, page.Limit)
	if err != nil {
		return nil, fmt.Errorf("listing clients: %w", err)
	}
	clients, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Client, error) {
		return scanClient(row)
	})
	if err != nil {
		return nil, fmt.Errorf("listing clients: %w", err)
	}
	return clients, nil
}

// AssignPolicy sets the client's policy and returns the updated client. An
// empty policyID removes the assignment. A missing client is
// domain.ErrClientNotFound, a missing policy domain.ErrPolicyNotFound, and
// a disabled one domain.ErrPolicyDisabled.
func (r *Clients) AssignPolicy(ctx context.Context, clientID, policyID string) (domain.Client, error) {
	if !domain.IsID(clientID) {
		return domain.Client{}, domain.ErrClientNotFound
	}
	if policyID != "" && !domain.IsID(policyID) {
		return domain.Client{}, domain.ErrPolicyNotFound
	}
	var c domain.Client
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// The client is checked first so that a request naming neither a
		// real client nor a real policy reports the client, which is the
		// resource in its URL.
		var locked string
		err := tx.QueryRow(ctx, `SELECT id FROM clients WHERE id = $1 FOR NO KEY UPDATE`, clientID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrClientNotFound
		}
		if err != nil {
			return fmt.Errorf("locking client: %w", err)
		}

		var policyParam *string
		if policyID != "" {
			if err := requireEnabledPolicy(ctx, tx, policyID); err != nil {
				return err
			}
			policyParam = &policyID
		}

		c, err = scanClient(tx.QueryRow(ctx, `
			UPDATE clients SET policy_id = $2, updated_at = now()
			WHERE id = $1
			RETURNING `+clientColumns,
			clientID, policyParam))
		return err
	})
	switch {
	case errors.Is(err, domain.ErrClientNotFound), errors.Is(err, domain.ErrPolicyNotFound), errors.Is(err, domain.ErrPolicyDisabled):
		return domain.Client{}, err
	case err != nil:
		return domain.Client{}, fmt.Errorf("assigning policy: %w", err)
	}
	return c, nil
}

// requireEnabledPolicy checks that the policy exists and is enabled, and
// holds a share lock on it until tx ends, so it cannot be disabled between
// this check and the assignment that relies on it.
func requireEnabledPolicy(ctx context.Context, tx pgx.Tx, policyID string) error {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM policies WHERE id = $1 FOR SHARE`, policyID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrPolicyNotFound
	}
	if err != nil {
		return fmt.Errorf("checking policy: %w", err)
	}
	if !enabled {
		return domain.ErrPolicyDisabled
	}
	return nil
}

// PolicyID returns the ID of the client's assigned policy, or "" when it
// has none. A missing client is domain.ErrClientNotFound.
func (r *Clients) PolicyID(ctx context.Context, clientID string) (string, error) {
	if !domain.IsID(clientID) {
		return "", domain.ErrClientNotFound
	}
	var policyID *string
	err := r.pool.QueryRow(ctx, `SELECT policy_id FROM clients WHERE id = $1`, clientID).Scan(&policyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrClientNotFound
	}
	if err != nil {
		return "", fmt.Errorf("selecting client policy: %w", err)
	}
	if policyID == nil {
		return "", nil
	}
	return *policyID, nil
}

func scanClient(row pgx.Row) (domain.Client, error) {
	var (
		c        domain.Client
		policyID *string
		status   string
	)
	err := row.Scan(&c.ID, &c.Name, &policyID, &status, &c.CreatedAt, &c.UpdatedAt)
	if policyID != nil {
		c.PolicyID = *policyID
	}
	c.Status = domain.ClientStatus(status)
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, err
}
