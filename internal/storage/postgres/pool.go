package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// applicationName labels RateGuard's sessions in pg_stat_activity.
const applicationName = "rateguard"

// errInvalidURL replaces the parser's own error, which can quote the
// connection string, password included.
var errInvalidURL = errors.New("database URL is not a valid PostgreSQL connection string")

// PoolConfig sizes and bounds the connection pool.
type PoolConfig struct {
	// URL is a postgres:// URL or a libpq key=value string. It usually
	// holds a password: never log it.
	URL             string
	MaxConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// NewPool builds a connection pool. It does not connect: connections open
// on first use, so the service can start while Postgres is still coming up
// and report the gap through /readyz instead of crash-looping.
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errInvalidURL
	}
	pcfg.MaxConns = cfg.MaxConns
	pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	if _, ok := pcfg.ConnConfig.RuntimeParams["application_name"]; !ok {
		pcfg.ConnConfig.RuntimeParams["application_name"] = applicationName
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	return pool, nil
}
