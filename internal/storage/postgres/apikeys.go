package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// keyColumns never includes key_hash: only FindByPrefix reads the digest.
const keyColumns = `id, client_id, key_prefix, created_at, revoked_at`

// APIKeys stores clients' API keys.
type APIKeys struct {
	pool *pgxpool.Pool
}

// NewAPIKeys returns an API key store on pool.
func NewAPIKeys(pool *pgxpool.Pool) *APIKeys {
	return &APIKeys{pool: pool}
}

// Create stores a new key for the client unless it already holds maxActive
// unrevoked keys, in which case it returns domain.ErrActiveKeyLimit. The
// client's row is locked for the duration, so concurrent issues for one
// client queue up and the limit holds under any interleaving.
func (r *APIKeys) Create(ctx context.Context, clientID string, key domain.NewAPIKey, maxActive int) (domain.APIKey, error) {
	if !domain.IsID(clientID) {
		return domain.APIKey{}, domain.ErrClientNotFound
	}
	var created domain.APIKey
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `SELECT id FROM clients WHERE id = $1 FOR UPDATE`, clientID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrClientNotFound
		}
		if err != nil {
			return fmt.Errorf("locking client: %w", err)
		}

		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE client_id = $1 AND revoked_at IS NULL`, clientID).Scan(&active); err != nil {
			return fmt.Errorf("counting active keys: %w", err)
		}
		if active >= maxActive {
			return domain.ErrActiveKeyLimit
		}

		created, err = insertKey(ctx, tx, clientID, key)
		return err
	})
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("issuing key: %w", err)
	}
	return created, nil
}

// List returns every key the client has held, active and revoked, oldest
// first. A client with no keys, or no such client, yields an empty list.
func (r *APIKeys) List(ctx context.Context, clientID string) ([]domain.APIKey, error) {
	if !domain.IsID(clientID) {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+keyColumns+` FROM api_keys WHERE client_id = $1 ORDER BY id`, clientID)
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.APIKey, error) {
		return scanKey(row)
	})
	if err != nil {
		return nil, fmt.Errorf("listing keys: %w", err)
	}
	return keys, nil
}

// Revoke marks the client's key revoked and returns it. Revoking a key
// that is already revoked keeps the original time and succeeds, so the
// call is idempotent. A key that does not exist or belongs to another
// client returns domain.ErrAPIKeyNotFound.
func (r *APIKeys) Revoke(ctx context.Context, clientID, keyID string) (domain.APIKey, error) {
	if !domain.IsID(clientID) || !domain.IsID(keyID) {
		return domain.APIKey{}, domain.ErrAPIKeyNotFound
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1 AND client_id = $2
		RETURNING `+keyColumns,
		keyID, clientID)
	k, err := scanKey(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.APIKey{}, domain.ErrAPIKeyNotFound
	}
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("revoking key: %w", err)
	}
	return k, nil
}

// FindByPrefix returns what authentication needs to check a key with the
// given public prefix, or domain.ErrAPIKeyNotFound. It is one indexed
// lookup on api_keys_key_prefix_key.
func (r *APIKeys) FindByPrefix(ctx context.Context, prefix string) (domain.StoredKey, error) {
	var (
		sk     domain.StoredKey
		status string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT k.id, k.client_id, k.key_prefix, k.key_hash, k.revoked_at IS NOT NULL, c.status
		FROM api_keys k JOIN clients c ON c.id = k.client_id
		WHERE k.key_prefix = $1`,
		prefix).Scan(&sk.KeyID, &sk.ClientID, &sk.Prefix, &sk.Hash, &sk.Revoked, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StoredKey{}, domain.ErrAPIKeyNotFound
	}
	if err != nil {
		return domain.StoredKey{}, fmt.Errorf("finding key: %w", err)
	}
	sk.ClientStatus = domain.ClientStatus(status)
	return sk, nil
}

// insertKey adds a key inside tx. A prefix collision returns
// domain.ErrAPIKeyPrefixTaken so the caller can retry with a fresh key.
func insertKey(ctx context.Context, tx pgx.Tx, clientID string, key domain.NewAPIKey) (domain.APIKey, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO api_keys (client_id, key_prefix, key_hash) VALUES ($1, $2, $3)
		RETURNING `+keyColumns,
		clientID, key.Prefix, key.Hash)
	k, err := scanKey(row)
	if isUniqueViolation(err, "api_keys_key_prefix_key") {
		return domain.APIKey{}, domain.ErrAPIKeyPrefixTaken
	}
	if err != nil {
		return domain.APIKey{}, fmt.Errorf("inserting key: %w", err)
	}
	return k, nil
}

func scanKey(row pgx.Row) (domain.APIKey, error) {
	var (
		k         domain.APIKey
		revokedAt *time.Time
	)
	err := row.Scan(&k.ID, &k.ClientID, &k.Prefix, &k.CreatedAt, &revokedAt)
	k.CreatedAt = k.CreatedAt.UTC()
	if revokedAt != nil {
		k.RevokedAt = revokedAt.UTC()
	}
	return k, err
}
