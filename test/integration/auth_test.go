//go:build integration

package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func TestAuthenticate(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	svc := newClientService(pool)
	auth := client.NewAuthenticator(postgres.NewAPIKeys(pool))

	c, issued, err := svc.Register(ctx, "Customer App")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	id, err := auth.Authenticate(ctx, issued.Plaintext)
	if err != nil {
		t.Fatalf("Authenticate(valid key): %v", err)
	}
	want := domain.Identity{Kind: domain.IdentityAPIKey, ClientID: c.ID, KeyPrefix: issued.Key.Prefix}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}

	// The same prefix with a different secret: the lookup succeeds, the
	// digest comparison must not.
	last := issued.Plaintext[len(issued.Plaintext)-1]
	swap := byte('a')
	if last == 'a' {
		swap = 'b'
	}
	tampered := issued.Plaintext[:len(issued.Plaintext)-1] + string(swap)

	invalid := map[string]string{
		"tampered secret": tampered,
		"unknown prefix":  newTestKey(t).Prefix + "_" + strings.Repeat("a", 52),
		"malformed":       "not-a-key",
		"empty":           "",
		"prefix only":     issued.Key.Prefix,
	}
	for name, key := range invalid {
		_, err := auth.Authenticate(ctx, key)
		if !errors.Is(err, domain.ErrInvalidAPIKey) {
			t.Errorf("%s: error = %v, want ErrInvalidAPIKey", name, err)
		}
	}
}

func TestAuthenticateRevokedAndDisabled(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	svc := newClientService(pool)
	auth := client.NewAuthenticator(postgres.NewAPIKeys(pool))

	c, issued, err := svc.Register(ctx, "Old App")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	second, err := svc.IssueKey(ctx, c.ID)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	if _, err := svc.RevokeKey(ctx, c.ID, issued.Key.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	if _, err := auth.Authenticate(ctx, issued.Plaintext); !errors.Is(err, domain.ErrInvalidAPIKey) {
		t.Errorf("revoked key: error = %v, want ErrInvalidAPIKey", err)
	}
	if _, err := auth.Authenticate(ctx, second.Plaintext); err != nil {
		t.Errorf("the other, unrevoked key: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE clients SET status = 'disabled' WHERE id = $1`, c.ID); err != nil {
		t.Fatalf("disabling client: %v", err)
	}
	if _, err := auth.Authenticate(ctx, second.Plaintext); !errors.Is(err, domain.ErrClientDisabled) {
		t.Errorf("key of a disabled client: error = %v, want ErrClientDisabled", err)
	}
}

func TestAuthenticateErrorsNeverContainTheKey(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	_, issued, err := newClientService(pool).Register(ctx, "Leaky")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	auth := client.NewAuthenticator(postgres.NewAPIKeys(pool))

	// A closed pool makes the lookup itself fail, the one error path that
	// carries detail.
	pool.Close()
	_, err = auth.Authenticate(ctx, issued.Plaintext)
	if err == nil || errors.Is(err, domain.ErrInvalidAPIKey) {
		t.Fatalf("Authenticate with storage down: error = %v, want a lookup failure", err)
	}
	secret := strings.TrimPrefix(issued.Plaintext, issued.Key.Prefix+"_")
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the secret part of the key: %v", err)
	}
	if !strings.Contains(err.Error(), issued.Key.Prefix) {
		t.Errorf("error = %v, want it to name the key by its prefix", err)
	}
}
