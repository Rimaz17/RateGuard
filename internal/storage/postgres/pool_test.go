package postgres_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func TestNewPoolAppliesLimitsWithoutConnecting(t *testing.T) {
	// Port 1 on localhost refuses connections, so this only passes if
	// NewPool leaves connecting to first use.
	pool, err := postgres.NewPool(t.Context(), postgres.PoolConfig{
		URL:             "postgres://rateguard:secret@127.0.0.1:1/rateguard?sslmode=disable",
		MaxConns:        7,
		MaxConnLifetime: 3 * time.Minute,
		MaxConnIdleTime: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	cfg := pool.Config()
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime != 3*time.Minute {
		t.Errorf("MaxConnLifetime = %s, want 3m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 45*time.Second {
		t.Errorf("MaxConnIdleTime = %s, want 45s", cfg.MaxConnIdleTime)
	}
	if got := cfg.ConnConfig.RuntimeParams["application_name"]; got != "rateguard" {
		t.Errorf("application_name = %q, want rateguard", got)
	}
}

func TestNewPoolKeepsExplicitApplicationName(t *testing.T) {
	pool, err := postgres.NewPool(t.Context(), postgres.PoolConfig{
		URL:             "postgres://u:p@127.0.0.1:1/db?application_name=custom",
		MaxConns:        1,
		MaxConnLifetime: time.Minute,
		MaxConnIdleTime: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	if got := pool.Config().ConnConfig.RuntimeParams["application_name"]; got != "custom" {
		t.Errorf("application_name = %q, want custom", got)
	}
}

func TestNewPoolInvalidURLDoesNotLeakPassword(t *testing.T) {
	urls := []string{
		"postgres://user:hunter2@db:notaport/app",
		"postgres://user:hunter2@[::1/app",
		"host=db password=hunter2 port=notaport",
	}
	for _, url := range urls {
		_, err := postgres.NewPool(t.Context(), postgres.PoolConfig{URL: url, MaxConns: 1})
		if err == nil {
			t.Errorf("NewPool(%q): want error, got nil", url)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("NewPool error leaks the password: %q", err)
		}
	}
}
