package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Rimaz17/RateGuard/migrations"
)

// migrateConnectTimeout stops the migrate command hanging on the operating
// system's TCP timeout when the database is unreachable and the connection
// string sets no connect_timeout of its own.
const migrateConnectTimeout = 10 * time.Second

// Migrator applies the schema migrations embedded in the binary. It holds
// one database connection; call Close when done.
type Migrator struct {
	m *migrate.Migrate
}

// NewMigrator connects to the database and prepares the embedded
// migrations. logger receives one line per applied migration.
func NewMigrator(ctx context.Context, databaseURL string, logger *slog.Logger) (*Migrator, error) {
	connCfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, errInvalidURL
	}
	if connCfg.ConnectTimeout == 0 {
		connCfg.ConnectTimeout = migrateConnectTimeout
	}
	db := stdlib.OpenDB(*connCfg)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // The ping error is the one worth reporting.
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		_ = db.Close() // The driver error is the one worth reporting.
		return nil, fmt.Errorf("preparing migration driver: %w", err)
	}
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		_ = driver.Close() // The source error is the one worth reporting.
		return nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		_ = source.Close()
		_ = driver.Close()
		return nil, fmt.Errorf("preparing migrations: %w", err)
	}
	m.Log = migrateLogger{logger}
	return &Migrator{m: m}, nil
}

// Up applies every pending migration. It is a no-op when the schema is
// already current.
func (mg *Migrator) Up(ctx context.Context) error {
	return mg.run(ctx, "up", mg.m.Up)
}

// Down reverts the most recent steps migrations.
func (mg *Migrator) Down(ctx context.Context, steps int) error {
	if steps < 1 {
		return fmt.Errorf("down needs at least 1 step, got %d", steps)
	}
	return mg.run(ctx, "down", func() error { return mg.m.Steps(-steps) })
}

// DownAll reverts every applied migration, leaving an empty schema. It
// destroys all data.
func (mg *Migrator) DownAll(ctx context.Context) error {
	return mg.run(ctx, "down all", mg.m.Down)
}

// Version reports the current schema version, and whether a failed
// migration left it dirty. Version 0 means no migration has been applied.
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading schema version: %w", err)
	}
	return version, dirty, nil
}

// Close releases the database connection.
func (mg *Migrator) Close() error {
	srcErr, dbErr := mg.m.Close()
	return errors.Join(srcErr, dbErr)
}

// run executes op and maps cancellation of ctx onto golang-migrate's
// graceful stop, which halts between migrations rather than mid-file. The
// library has no context support of its own.
func (mg *Migrator) run(ctx context.Context, name string, op func() error) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case mg.m.GracefulStop <- true:
			default: // A stop is already pending.
			}
		case <-done:
		}
	}()

	err := op()
	if errors.Is(err, migrate.ErrNoChange) {
		err = nil
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("migrating %s: %w", name, err)
	}
	return nil
}

// migrateLogger adapts slog to golang-migrate's Printf-style logger.
type migrateLogger struct {
	logger *slog.Logger
}

func (l migrateLogger) Printf(format string, v ...any) {
	l.logger.Info("migration", slog.String("detail", strings.TrimSpace(fmt.Sprintf(format, v...))))
}

func (l migrateLogger) Verbose() bool { return false }
