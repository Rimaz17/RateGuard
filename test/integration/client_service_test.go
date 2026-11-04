//go:build integration

package integration

import (
	"crypto/sha256"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

var fullKeyShape = regexp.MustCompile(`^rg_[a-z0-9]{1,3}_[a-z2-7]{16}_[a-z2-7]{52}$`)

func newClientService(pool *pgxpool.Pool) *client.Service {
	return client.New(postgres.NewClients(pool), postgres.NewAPIKeys(pool))
}

func TestServiceRegisterIssuesAWorkingKey(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	svc := newClientService(pool)

	c, issued, err := svc.Register(ctx, "Customer App")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if c.Name != "Customer App" || c.Status != domain.ClientActive {
		t.Errorf("client = %+v, want an active Customer App", c)
	}
	if !fullKeyShape.MatchString(issued.Plaintext) {
		t.Errorf("key %q does not have the rg_<tag>_<lookup>_<secret> shape", issued.Plaintext)
	}
	if !strings.HasPrefix(issued.Plaintext, "rg_cus_") || !strings.HasPrefix(issued.Plaintext, issued.Key.Prefix+"_") {
		t.Errorf("key %q should start with its prefix %q and the rg_cus_ tag", issued.Plaintext, issued.Key.Prefix)
	}
	if issued.Key.ClientID != c.ID || !issued.Key.Active() {
		t.Errorf("issued key = %+v, want an active key for %s", issued.Key, c.ID)
	}

	// Storage holds the digest of exactly this key, and nothing else of it.
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT key_hash FROM api_keys WHERE id = $1`, issued.Key.ID).Scan(&hash); err != nil {
		t.Fatalf("reading digest: %v", err)
	}
	if want := sha256.Sum256([]byte(issued.Plaintext)); string(hash) != string(want[:]) {
		t.Error("stored digest is not SHA-256 of the issued key")
	}
}

func TestServiceKeyLifecycle(t *testing.T) {
	ctx := t.Context()
	svc := newClientService(migratedPool(t))
	c, first, err := svc.Register(ctx, "Merchant App")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	second, err := svc.IssueKey(ctx, c.ID)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if second.Plaintext == first.Plaintext || second.Key.Prefix == first.Key.Prefix {
		t.Error("the rotated key repeats the first one")
	}
	if !strings.HasPrefix(second.Plaintext, "rg_mer_") {
		t.Errorf("rotated key %q should carry the client's tag", second.Plaintext)
	}

	if _, err := svc.IssueKey(ctx, c.ID); !errors.Is(err, domain.ErrActiveKeyLimit) {
		t.Errorf("third IssueKey: error = %v, want ErrActiveKeyLimit", err)
	}

	revoked, err := svc.RevokeKey(ctx, c.ID, first.Key.ID)
	if err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if revoked.Active() {
		t.Error("RevokeKey returned a key that is still active")
	}

	keys, err := svc.ListKeys(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 || keys[0].ID != first.Key.ID || keys[0].Active() || keys[1].ID != second.Key.ID || !keys[1].Active() {
		t.Errorf("ListKeys = %+v, want the revoked first key, then the active second", keys)
	}
}

func TestServiceUnknownClient(t *testing.T) {
	ctx := t.Context()
	svc := newClientService(migratedPool(t))
	const missing = "01a1a2fb-68ca-772e-a1e6-072972830b19"

	if _, err := svc.Get(ctx, missing); !errors.Is(err, domain.ErrClientNotFound) {
		t.Errorf("Get: error = %v, want ErrClientNotFound", err)
	}
	if _, err := svc.IssueKey(ctx, missing); !errors.Is(err, domain.ErrClientNotFound) {
		t.Errorf("IssueKey: error = %v, want ErrClientNotFound", err)
	}
	if _, err := svc.ListKeys(ctx, missing); !errors.Is(err, domain.ErrClientNotFound) {
		t.Errorf("ListKeys: error = %v, want ErrClientNotFound", err)
	}
	if _, err := svc.RevokeKey(ctx, missing, missing); !errors.Is(err, domain.ErrAPIKeyNotFound) {
		t.Errorf("RevokeKey: error = %v, want ErrAPIKeyNotFound", err)
	}
}

func TestServiceListPages(t *testing.T) {
	ctx := t.Context()
	svc := newClientService(migratedPool(t))
	for _, name := range []string{"A", "B", "C"} {
		if _, _, err := svc.Register(ctx, name); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	page, err := svc.List(ctx, domain.Page{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 2 || page[0].Name != "A" || page[1].Name != "B" {
		t.Errorf("List = %+v, want A and B", page)
	}
}
