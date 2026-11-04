package postgres_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

// The happy paths need a real database and live in test/integration.

func TestNewMigratorInvalidURLDoesNotLeakPassword(t *testing.T) {
	_, err := postgres.NewMigrator(t.Context(), "postgres://user:hunter2@db:notaport/app", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("NewMigrator: want error, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("NewMigrator error leaks the password: %q", err)
	}
}

func TestNewMigratorUnreachableDatabase(t *testing.T) {
	// Port 1 on localhost refuses connections immediately.
	_, err := postgres.NewMigrator(t.Context(), "postgres://user:hunter2@127.0.0.1:1/app?sslmode=disable", slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("NewMigrator: want error, got nil")
	}
	if !strings.Contains(err.Error(), "connecting to database") {
		t.Errorf("NewMigrator error = %q, want it to say it could not connect", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("NewMigrator error leaks the password: %q", err)
	}
}
