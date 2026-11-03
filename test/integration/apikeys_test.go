//go:build integration

package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

var testKeyEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// newTestKey builds a storable key the way the client package does, without
// reaching into its internals: a well-formed prefix and a 32-byte digest.
func newTestKey(t *testing.T) domain.NewAPIKey {
	t.Helper()
	var lookup [10]byte
	_, _ = rand.Read(lookup[:])
	digest := sha256.Sum256([]byte(rand.Text()))
	return domain.NewAPIKey{Prefix: "rg_tst_" + testKeyEncoding.EncodeToString(lookup[:]), Hash: digest[:]}
}

func registerClient(t *testing.T, pool *pgxpool.Pool, name string) (domain.Client, domain.APIKey, domain.NewAPIKey) {
	t.Helper()
	nk := newTestKey(t)
	c, k, err := postgres.NewClients(pool).CreateWithKey(t.Context(), name, nk)
	if err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	return c, k, nk
}

func TestCreateWithKeyStoresBothRows(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	keys := postgres.NewAPIKeys(pool)

	c, k, nk := registerClient(t, pool, "Customer App")

	if k.ClientID != c.ID || k.Prefix != nk.Prefix || !k.Active() || !domain.IsID(k.ID) {
		t.Errorf("first key = %+v, want an active key for client %s with prefix %s", k, c.ID, nk.Prefix)
	}
	if k.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt is in %v, want UTC", k.CreatedAt.Location())
	}

	sk, err := keys.FindByPrefix(ctx, nk.Prefix)
	if err != nil {
		t.Fatalf("FindByPrefix: %v", err)
	}
	if sk.KeyID != k.ID || sk.ClientID != c.ID || !bytes.Equal(sk.Hash, nk.Hash) || sk.Revoked || sk.ClientStatus != domain.ClientActive {
		t.Errorf("FindByPrefix = %+v, want the stored digest for an active key on an active client", sk)
	}
}

func TestCreateWithKeyRollsBackOnPrefixCollision(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	_, _, taken := registerClient(t, pool, "First")

	collide := newTestKey(t)
	collide.Prefix = taken.Prefix
	_, _, err := postgres.NewClients(pool).CreateWithKey(ctx, "Second", collide)
	if !errors.Is(err, domain.ErrAPIKeyPrefixTaken) {
		t.Fatalf("CreateWithKey with a taken prefix: error = %v, want ErrAPIKeyPrefixTaken", err)
	}

	var clients int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM clients`).Scan(&clients); err != nil {
		t.Fatalf("counting clients: %v", err)
	}
	if clients != 1 {
		t.Errorf("clients = %d after the failed registration, want 1: the client row must roll back with its key", clients)
	}
}

func TestCreateKeyEnforcesTwoActiveKeys(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	keys := postgres.NewAPIKeys(pool)
	c, first, _ := registerClient(t, pool, "Merchant App")

	second, err := keys.Create(ctx, c.ID, newTestKey(t), domain.MaxActiveKeys)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if _, err := keys.Create(ctx, c.ID, newTestKey(t), domain.MaxActiveKeys); !errors.Is(err, domain.ErrActiveKeyLimit) {
		t.Fatalf("third key: error = %v, want ErrActiveKeyLimit", err)
	}

	if _, err := keys.Revoke(ctx, c.ID, first.ID); err != nil {
		t.Fatalf("revoking first key: %v", err)
	}
	third, err := keys.Create(ctx, c.ID, newTestKey(t), domain.MaxActiveKeys)
	if err != nil {
		t.Fatalf("third key after a revoke: %v", err)
	}

	listed, err := keys.List(ctx, c.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 3 || listed[0].ID != first.ID || listed[1].ID != second.ID || listed[2].ID != third.ID {
		t.Fatalf("List = %+v, want first, second and third in creation order", listed)
	}
	if listed[0].Active() || !listed[1].Active() || !listed[2].Active() {
		t.Errorf("active flags = %v %v %v, want false true true", listed[0].Active(), listed[1].Active(), listed[2].Active())
	}
}

// TestCreateKeyLimitHoldsUnderConcurrency races many issues for one client.
// Without the row lock, several transactions could each count one active
// key and all insert.
func TestCreateKeyLimitHoldsUnderConcurrency(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	keys := postgres.NewAPIKeys(pool)
	c, _, _ := registerClient(t, pool, "Busy App")

	const attempts = 64
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok      int
		limited int
		other   []error
	)
	// Open every pooled connection first: dialling is slower than a whole
	// issue transaction, so on a cold pool the first issue would commit
	// before a second could start and the race would never happen.
	warm := make([]*pgxpool.Conn, 0, pool.Config().MaxConns)
	for range pool.Config().MaxConns {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("warming pool: %v", err)
		}
		warm = append(warm, conn)
	}
	for _, conn := range warm {
		conn.Release()
	}

	// Release every goroutine at once so their transactions overlap.
	start := make(chan struct{})
	for range attempts {
		nk := newTestKey(t)
		wg.Go(func() {
			<-start
			_, err := keys.Create(ctx, c.ID, nk, domain.MaxActiveKeys)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, domain.ErrActiveKeyLimit):
				limited++
			default:
				other = append(other, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if ok != 1 || limited != attempts-1 {
		t.Errorf("%d issued and %d refused, want 1 and %d (the client already had one key)", ok, limited, attempts-1)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE client_id = $1 AND revoked_at IS NULL`, c.ID).Scan(&active); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if active != domain.MaxActiveKeys {
		t.Errorf("active keys = %d, want %d", active, domain.MaxActiveKeys)
	}
}

func TestCreateKeyForMissingClient(t *testing.T) {
	keys := postgres.NewAPIKeys(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, err := keys.Create(t.Context(), id, newTestKey(t), domain.MaxActiveKeys); !errors.Is(err, domain.ErrClientNotFound) {
			t.Errorf("Create for client %q: error = %v, want ErrClientNotFound", id, err)
		}
	}
}

func TestRevokeIsIdempotentAndScopedToTheClient(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	keys := postgres.NewAPIKeys(pool)
	a, aKey, aNew := registerClient(t, pool, "A")
	b, _, _ := registerClient(t, pool, "B")

	if _, err := keys.Revoke(ctx, b.ID, aKey.ID); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("revoking A's key as B: error = %v, want ErrAPIKeyNotFound", err)
	}

	first, err := keys.Revoke(ctx, a.ID, aKey.ID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if first.Active() || first.RevokedAt.Before(first.CreatedAt) {
		t.Errorf("revoked key = %+v, want a revocation time after creation", first)
	}
	again, err := keys.Revoke(ctx, a.ID, aKey.ID)
	if err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	if !again.RevokedAt.Equal(first.RevokedAt) {
		t.Errorf("second Revoke moved RevokedAt from %v to %v, want it kept", first.RevokedAt, again.RevokedAt)
	}

	sk, err := keys.FindByPrefix(ctx, aNew.Prefix)
	if err != nil {
		t.Fatalf("FindByPrefix: %v", err)
	}
	if !sk.Revoked {
		t.Error("FindByPrefix reports a revoked key as not revoked")
	}

	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		if _, err := keys.Revoke(ctx, a.ID, id); !errors.Is(err, domain.ErrAPIKeyNotFound) {
			t.Errorf("Revoke(%q): error = %v, want ErrAPIKeyNotFound", id, err)
		}
	}
}

func TestFindByPrefixReportsDisabledClientsAndMisses(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	keys := postgres.NewAPIKeys(pool)
	c, _, nk := registerClient(t, pool, "Old App")

	// Disabling a client has no API yet; set it directly.
	if _, err := pool.Exec(ctx, `UPDATE clients SET status = 'disabled' WHERE id = $1`, c.ID); err != nil {
		t.Fatalf("disabling client: %v", err)
	}
	sk, err := keys.FindByPrefix(ctx, nk.Prefix)
	if err != nil {
		t.Fatalf("FindByPrefix: %v", err)
	}
	if sk.ClientStatus != domain.ClientDisabled {
		t.Errorf("ClientStatus = %q, want disabled", sk.ClientStatus)
	}

	if _, err := keys.FindByPrefix(ctx, newTestKey(t).Prefix); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("FindByPrefix of an unknown prefix: error = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestListKeysOfUnknownClientIsEmpty(t *testing.T) {
	keys := postgres.NewAPIKeys(migratedPool(t))
	for _, id := range []string{"01a1a2fb-68ca-772e-a1e6-072972830b19", "nope"} {
		got, err := keys.List(t.Context(), id)
		if err != nil || len(got) != 0 {
			t.Errorf("List(%q) = %v, %v; want empty, nil", id, got, err)
		}
	}
}

func TestKeyHashIsTheOnlySecretStored(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	_, _, nk := registerClient(t, pool, "Audit")

	var prefix string
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT key_prefix, key_hash FROM api_keys`).Scan(&prefix, &hash); err != nil {
		t.Fatalf("reading row: %v", err)
	}
	if prefix != nk.Prefix || !bytes.Equal(hash, nk.Hash) || len(hash) != sha256.Size {
		t.Errorf("stored (%q, %x), want exactly the prefix and 32-byte digest handed to storage", prefix, hash)
	}
}
