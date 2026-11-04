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
