//go:build integration

package integration

import (
	"context"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	"github.com/Rimaz17/RateGuard/migrations"
)

func newMigrator(t *testing.T, dbURL string) *postgres.Migrator {
	t.Helper()
	mg, err := postgres.NewMigrator(t.Context(), dbURL, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("creating migrator: %v", err)
	}
	t.Cleanup(func() {
		if err := mg.Close(); err != nil {
			t.Errorf("closing migrator: %v", err)
		}
	})
	return mg
}

// embeddedVersions counts the migrations compiled into the binary, and
// checks that every up file has a matching down file.
func embeddedVersions(t *testing.T) int {
	t.Helper()
	ups, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		t.Fatalf("listing migrations: %v", err)
	}
	for _, up := range ups {
		down := strings.TrimSuffix(up, ".up.sql") + ".down.sql"
		if _, err := fs.Stat(migrations.FS, down); err != nil {
			t.Fatalf("%s has no matching %s", up, down)
		}
	}
	if len(ups) == 0 {
		t.Fatal("no migrations are embedded")
	}
	return len(ups)
}

// schemaTables lists the tables in the public schema, leaving out
// golang-migrate's bookkeeping table.
func schemaTables(ctx context.Context, t *testing.T, dbURL string) []string {
	t.Helper()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("listing tables: %v", err)
	}
	return tables
}

// TestMigrationsUpDownUp is the CI check that every migration applies and
// reverts cleanly against a fresh database, one step at a time, and that a
// full round trip leaves the same schema as a single up.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := t.Context()
	dbURL := newDatabase(t)
	mg := newMigrator(t, dbURL)
	want := embeddedVersions(t)

	if v, dirty, err := mg.Version(); err != nil || v != 0 || dirty {
		t.Fatalf("fresh database: Version() = %d, %v, %v; want 0, false, nil", v, dirty, err)
	}

	if err := mg.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if v, dirty, err := mg.Version(); err != nil || int(v) != want || dirty {
		t.Fatalf("after up: Version() = %d, %v, %v; want %d, false, nil", v, dirty, err, want)
	}
	afterUp := schemaTables(ctx, t, dbURL)
	if len(afterUp) == 0 {
		t.Fatal("after up: no tables in the public schema")
	}

	if err := mg.Up(ctx); err != nil {
		t.Errorf("second up on a current schema: %v, want a no-op", err)
	}

	for v := want; v > 0; v-- {
		if err := mg.Down(ctx, 1); err != nil {
			t.Fatalf("down from version %d: %v", v, err)
		}
	}
	if v, _, err := mg.Version(); err != nil || v != 0 {
		t.Fatalf("after down: Version() = %d, %v; want 0, nil", v, err)
	}
	if tables := schemaTables(ctx, t, dbURL); len(tables) != 0 {
		t.Fatalf("after down: tables remain: %v", tables)
	}

	if err := mg.Down(ctx, 1); err == nil {
		t.Error("down with nothing applied: want error, got nil")
	}

	if err := mg.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if again := schemaTables(ctx, t, dbURL); !slices.Equal(again, afterUp) {
		t.Errorf("tables after the round trip = %v, want %v", again, afterUp)
	}
}

func TestMigrationsDownAll(t *testing.T) {
	ctx := t.Context()
	dbURL := newDatabase(t)
	mg := newMigrator(t, dbURL)

	if err := mg.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := mg.DownAll(ctx); err != nil {
		t.Fatalf("down all: %v", err)
	}
	if tables := schemaTables(ctx, t, dbURL); len(tables) != 0 {
		t.Errorf("after down all: tables remain: %v", tables)
	}
}

func TestMigrationsDownTooFar(t *testing.T) {
	ctx := t.Context()
	dbURL := newDatabase(t)
	mg := newMigrator(t, dbURL)
	n := embeddedVersions(t)

	if err := mg.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	err := mg.Down(ctx, n+3)
	if err == nil || !strings.Contains(err.Error(), "3 fewer than") {
		t.Errorf("Down(%d) error = %v, want the 3-step shortfall reported", n+3, err)
	}
	if v, _, err := mg.Version(); err != nil || v != 0 {
		t.Errorf("after overshooting down: Version() = %d, %v; want 0, nil", v, err)
	}
}

func TestMigrationsStopWhenCancelled(t *testing.T) {
	dbURL := newDatabase(t)
	mg := newMigrator(t, dbURL)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := mg.Up(ctx); err == nil {
		t.Error("Up with a cancelled context: want error, got nil")
	}
	if v, _, err := mg.Version(); err != nil || v != 0 {
		t.Errorf("after a cancelled up: Version() = %d, %v; want 0, nil", v, err)
	}
}
