// Package config loads and validates RateGuard's runtime configuration from
// environment variables. Every variable is prefixed RG_ and documented in
// configs/.env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

// MinAdminTokenLen is the shortest admin token accepted. 32 characters of
// `openssl rand -hex 32` output carry 128 bits.
const MinAdminTokenLen = 32

// Config is the complete runtime configuration for one RateGuard instance.
type Config struct {
	// HTTPAddr is the TCP address the HTTP server listens on.
	HTTPAddr string `env:"RG_HTTP_ADDR" envDefault:":8080"`

	// The http.Server timeouts. An unbounded server is a slow-client DoS
	// waiting to happen, so all of them must be positive.
	HTTPReadHeaderTimeout time.Duration `env:"RG_HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	HTTPReadTimeout       time.Duration `env:"RG_HTTP_READ_TIMEOUT" envDefault:"10s"`
	HTTPWriteTimeout      time.Duration `env:"RG_HTTP_WRITE_TIMEOUT" envDefault:"10s"`
	HTTPIdleTimeout       time.Duration `env:"RG_HTTP_IDLE_TIMEOUT" envDefault:"120s"`

	// ShutdownTimeout bounds how long in-flight requests get to finish after
	// SIGINT or SIGTERM before the process exits anyway.
	ShutdownTimeout time.Duration `env:"RG_SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// LogLevel accepts debug, info, warn or error.
	LogLevel slog.Level `env:"RG_LOG_LEVEL" envDefault:"info"`

	// ReadinessTimeout bounds each dependency check behind /readyz, so a
	// hung dependency makes the probe fail fast instead of hanging it.
	ReadinessTimeout time.Duration `env:"RG_READINESS_TIMEOUT" envDefault:"2s"`

	// AdminToken is the bearer token the admin API (/v1/clients and,
	// later, /v1/policies) requires (ADR 0008). There is no default: an
	// admin API that mints keys must never be open by accident.
	AdminToken Secret `env:"RG_ADMIN_TOKEN,required,notEmpty"`

	Database Database
	Redis    Redis
}

// Redis configures the Redis client.
type Redis struct {
	// URL is a redis:// or rediss:// URL. It can carry a password, so it is
	// a Secret even though the default has none.
	URL Secret `env:"RG_REDIS_URL" envDefault:"redis://localhost:6379/0"`
}

// Database configures the PostgreSQL connection pool. It is a separate type
// so that the migrate subcommand can load it without the serving settings.
type Database struct {
	// URL is a libpq connection string or postgres:// URL. It usually holds
	// a password, so it is a Secret. There is no default: pointing a
	// service at the wrong database silently is worse than refusing to
	// start.
	URL Secret `env:"RG_DATABASE_URL,required,notEmpty"`

	// MaxConns caps the pool. The admin API is the only Postgres caller, so
	// a small pool is plenty.
	MaxConns int32 `env:"RG_DATABASE_MAX_CONNS" envDefault:"10"`

	// MaxConnLifetime recycles connections so that a failover or a
	// rebalanced proxy is picked up without a restart.
	MaxConnLifetime time.Duration `env:"RG_DATABASE_MAX_CONN_LIFETIME" envDefault:"30m"`

	// MaxConnIdleTime closes connections that sit unused.
	MaxConnIdleTime time.Duration `env:"RG_DATABASE_MAX_CONN_IDLE_TIME" envDefault:"5m"`
}

// Load reads the configuration from the process environment, applies
// defaults and validates the result. A variable that is set but empty is
// treated as unset and takes its default, so compose files can leave
// placeholders blank.
func Load() (Config, error) {
	return load[Config]()
}

// LoadDatabase reads and validates only the database settings. The migrate
// subcommand uses it so that it needs nothing else in its environment.
func LoadDatabase() (Database, error) {
	return load[Database]()
}

type validator interface {
	validate() error
}

func load[T validator]() (T, error) {
	cfg, err := env.ParseAs[T]()
	if err != nil {
		var zero T
		return zero, fmt.Errorf("parsing environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		var zero T
		return zero, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	errs := positiveDurations(
		namedDuration{"RG_HTTP_READ_HEADER_TIMEOUT", c.HTTPReadHeaderTimeout},
		namedDuration{"RG_HTTP_READ_TIMEOUT", c.HTTPReadTimeout},
		namedDuration{"RG_HTTP_WRITE_TIMEOUT", c.HTTPWriteTimeout},
		namedDuration{"RG_HTTP_IDLE_TIMEOUT", c.HTTPIdleTimeout},
		namedDuration{"RG_SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
		namedDuration{"RG_READINESS_TIMEOUT", c.ReadinessTimeout},
	)
	if n := len(c.AdminToken.Value()); n > 0 && n < MinAdminTokenLen {
		errs = append(errs, fmt.Errorf("RG_ADMIN_TOKEN must be at least %d characters, got %d", MinAdminTokenLen, n))
	}
	errs = append(errs, c.Database.validate())
	return errors.Join(errs...)
}

func (d Database) validate() error {
	errs := positiveDurations(
		namedDuration{"RG_DATABASE_MAX_CONN_LIFETIME", d.MaxConnLifetime},
		namedDuration{"RG_DATABASE_MAX_CONN_IDLE_TIME", d.MaxConnIdleTime},
	)
	if d.MaxConns < 1 {
		errs = append(errs, fmt.Errorf("RG_DATABASE_MAX_CONNS must be at least 1, got %d", d.MaxConns))
	}
	return errors.Join(errs...)
}

type namedDuration struct {
	name  string
	value time.Duration
}

func positiveDurations(ds ...namedDuration) []error {
	var errs []error
	for _, d := range ds {
		if d.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", d.name, d.value))
		}
	}
	return errs
}
