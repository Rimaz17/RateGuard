// Package config loads and validates RateGuard's runtime configuration from
// environment variables. Every variable is prefixed RG_ and documented in
// .env.example.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/caarlos0/env/v11"
)

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
}

// Load reads the configuration from the process environment, applies
// defaults and validates the result. A variable that is set but empty is
// treated as unset and takes its default, so compose files can leave
// placeholders blank.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parsing environment: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	durations := []struct {
		name  string
		value time.Duration
	}{
		{"RG_HTTP_READ_HEADER_TIMEOUT", c.HTTPReadHeaderTimeout},
		{"RG_HTTP_READ_TIMEOUT", c.HTTPReadTimeout},
		{"RG_HTTP_WRITE_TIMEOUT", c.HTTPWriteTimeout},
		{"RG_HTTP_IDLE_TIMEOUT", c.HTTPIdleTimeout},
		{"RG_SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
	}
	for _, d := range durations {
		if d.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", d.name, d.value))
		}
	}
	return errors.Join(errs...)
}
