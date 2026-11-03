//go:build integration

// Package integration exercises RateGuard against real PostgreSQL and Redis
// containers started by testcontainers-go. Nothing here is mocked: Lua
// semantics, transactions and constraint behaviour are exactly what these
// tests exist to catch. Run with:
//
//	go test -race -tags integration ./test/integration/...
//
// Docker must be reachable. The suite starts one shared Postgres container
// and gives each test its own freshly created database.
package integration

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

// postgresImage must match docker-compose.yml so the tests run against what
// developers run.
const postgresImage = "postgres:18.6-alpine"

// sharedPostgresURL points at the suite's Postgres container as the
// superuser. TestMain sets it once before any test runs; tests only read it.
var sharedPostgresURL string

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	ctx := context.Background()
	ctr, pgURL, err := startPostgres(ctx)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			log.Printf("terminating postgres container: %v", err)
		}
	}()
	if err != nil {
		log.Printf("starting postgres container: %v", err)
		return 1
	}
	sharedPostgresURL = pgURL
	return m.Run()
}

// startPostgres runs a Postgres container with a random password and
// returns it with a superuser URL for its "rateguard" database. The
// container is returned even on error so the caller can terminate it.
func startPostgres(ctx context.Context) (*testcontainers.DockerContainer, string, error) {
	password := rand.Text()
	ctr, err := testcontainers.Run(ctx, postgresImage,
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_USER":     "rateguard",
			"POSTGRES_PASSWORD": password,
			"POSTGRES_DB":       "rateguard",
		}),
		testcontainers.WithExposedPorts("5432/tcp"),
		// The entrypoint starts Postgres twice (init, then for real), so
		// wait for the second "ready" line and for the port.
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		return ctr, "", fmt.Errorf("running %s: %w", postgresImage, err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return ctr, "", fmt.Errorf("resolving postgres endpoint: %w", err)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("rateguard", password),
		Host:     endpoint,
		Path:     "/rateguard",
		RawQuery: "sslmode=disable",
	}
	return ctr, u.String(), nil
}

// newDatabase creates an empty database on the shared container, drops it
// when the test ends, and returns its URL.
func newDatabase(t *testing.T) string {
	t.Helper()
	name := "rg_test_" + strings.ToLower(rand.Text())

	conn, err := pgx.Connect(t.Context(), sharedPostgresURL)
	if err != nil {
		t.Fatalf("connecting to postgres: %v", err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("creating database %s: %v", name, err)
	}

	t.Cleanup(func() {
		// t.Context is already cancelled by the time cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, sharedPostgresURL)
		if err != nil {
			t.Errorf("connecting to drop database %s: %v", name, err)
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("dropping database %s: %v", name, err)
		}
	})

	u, err := url.Parse(sharedPostgresURL)
	if err != nil {
		t.Fatalf("parsing postgres URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// migratedPool creates a fresh database, applies every migration and returns
// a pool on it that is closed when the test ends.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := newDatabase(t)

	if err := newMigrator(t, dbURL).Up(t.Context()); err != nil {
		t.Fatalf("migrating up: %v", err)
	}

	pool, err := postgres.NewPool(t.Context(), postgres.PoolConfig{
		URL:             dbURL,
		MaxConns:        8,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: time.Minute,
	})
	if err != nil {
		t.Fatalf("creating pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
