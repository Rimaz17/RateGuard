package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/config"
)

// These stand in for the variables that have no default.
const (
	testDatabaseURL = "postgres://rateguard:secret@localhost:5432/rateguard"
	testAdminToken  = "test-admin-token-0123456789abcdef"
)

// setRequired sets every variable that has no default, so that each test can
// focus on the variables it is about.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("RG_DATABASE_URL", testDatabaseURL)
	t.Setenv("RG_ADMIN_TOKEN", testAdminToken)
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with only required variables: %v", err)
	}

	want := config.Config{
		HTTPAddr:              ":8080",
		HTTPReadHeaderTimeout: 5 * time.Second,
		HTTPReadTimeout:       10 * time.Second,
		HTTPWriteTimeout:      10 * time.Second,
		HTTPIdleTimeout:       120 * time.Second,
		ShutdownTimeout:       15 * time.Second,
		LogLevel:              slog.LevelInfo,
		ReadinessTimeout:      2 * time.Second,
		AdminToken:            testAdminToken,
		Database: config.Database{
			URL:             testDatabaseURL,
			MaxConns:        10,
			MaxConnLifetime: 30 * time.Minute,
			MaxConnIdleTime: 5 * time.Minute,
		},
		Redis: config.Redis{URL: "redis://localhost:6379/0"},
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("RG_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("RG_HTTP_WRITE_TIMEOUT", "250ms")
	t.Setenv("RG_SHUTDOWN_TIMEOUT", "1m")
	t.Setenv("RG_LOG_LEVEL", "debug")
	t.Setenv("RG_DATABASE_MAX_CONNS", "25")
	t.Setenv("RG_DATABASE_MAX_CONN_IDLE_TIME", "90s")
	t.Setenv("RG_REDIS_URL", "redis://:pw@cache:6380/2")
	t.Setenv("RG_READINESS_TIMEOUT", "750ms")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "127.0.0.1:9090")
	}
	if cfg.HTTPWriteTimeout != 250*time.Millisecond {
		t.Errorf("HTTPWriteTimeout = %s, want 250ms", cfg.HTTPWriteTimeout)
	}
	if cfg.ShutdownTimeout != time.Minute {
		t.Errorf("ShutdownTimeout = %s, want 1m", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %s, want DEBUG", cfg.LogLevel)
	}
	if cfg.Database.MaxConns != 25 {
		t.Errorf("Database.MaxConns = %d, want 25", cfg.Database.MaxConns)
	}
	if cfg.Database.MaxConnIdleTime != 90*time.Second {
		t.Errorf("Database.MaxConnIdleTime = %s, want 1m30s", cfg.Database.MaxConnIdleTime)
	}
	if got := cfg.Redis.URL.Value(); got != "redis://:pw@cache:6380/2" {
		t.Errorf("Redis.URL = %q, want %q", got, "redis://:pw@cache:6380/2")
	}
	if cfg.ReadinessTimeout != 750*time.Millisecond {
		t.Errorf("ReadinessTimeout = %s, want 750ms", cfg.ReadinessTimeout)
	}
}

func TestLoadEmptyValueUsesDefault(t *testing.T) {
	setRequired(t)
	t.Setenv("RG_HTTP_ADDR", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default %q", cfg.HTTPAddr, ":8080")
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"zero read header timeout", "RG_HTTP_READ_HEADER_TIMEOUT", "0s", "RG_HTTP_READ_HEADER_TIMEOUT must be positive"},
		{"negative read timeout", "RG_HTTP_READ_TIMEOUT", "-1s", "RG_HTTP_READ_TIMEOUT must be positive"},
		{"zero write timeout", "RG_HTTP_WRITE_TIMEOUT", "0", "RG_HTTP_WRITE_TIMEOUT must be positive"},
		{"zero idle timeout", "RG_HTTP_IDLE_TIMEOUT", "0s", "RG_HTTP_IDLE_TIMEOUT must be positive"},
		{"zero shutdown timeout", "RG_SHUTDOWN_TIMEOUT", "0s", "RG_SHUTDOWN_TIMEOUT must be positive"},
		{"unparseable duration", "RG_HTTP_IDLE_TIMEOUT", "soon", "parsing environment"},
		{"unknown log level", "RG_LOG_LEVEL", "loud", "parsing environment"},
		{"empty database url", "RG_DATABASE_URL", "", "RG_DATABASE_URL"},
		{"zero max conns", "RG_DATABASE_MAX_CONNS", "0", "RG_DATABASE_MAX_CONNS must be at least 1"},
		{"max conns overflows int32", "RG_DATABASE_MAX_CONNS", "3000000000", "parsing environment"},
		{"zero conn lifetime", "RG_DATABASE_MAX_CONN_LIFETIME", "0s", "RG_DATABASE_MAX_CONN_LIFETIME must be positive"},
		{"negative conn idle time", "RG_DATABASE_MAX_CONN_IDLE_TIME", "-5m", "RG_DATABASE_MAX_CONN_IDLE_TIME must be positive"},
		{"zero readiness timeout", "RG_READINESS_TIMEOUT", "0s", "RG_READINESS_TIMEOUT must be positive"},
		{"empty admin token", "RG_ADMIN_TOKEN", "", "RG_ADMIN_TOKEN"},
		{"short admin token", "RG_ADMIN_TOKEN", "too-short", "RG_ADMIN_TOKEN must be at least 32 characters, got 9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tt.key, tt.value)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q: want error, got nil", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("RG_ADMIN_TOKEN", testAdminToken)
	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() without RG_DATABASE_URL: want error, got nil")
	}
	if !strings.Contains(err.Error(), "RG_DATABASE_URL") {
		t.Errorf("Load() error = %q, want it to name RG_DATABASE_URL", err)
	}
}

func TestLoadRequiresAdminToken(t *testing.T) {
	t.Setenv("RG_DATABASE_URL", testDatabaseURL)
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "RG_ADMIN_TOKEN") {
		t.Fatalf("Load() without RG_ADMIN_TOKEN: error = %v, want one naming RG_ADMIN_TOKEN", err)
	}
}

func TestLoadAcceptsMinimumLengthAdminToken(t *testing.T) {
	setRequired(t)
	t.Setenv("RG_ADMIN_TOKEN", strings.Repeat("x", config.MinAdminTokenLen))
	if _, err := config.Load(); err != nil {
		t.Errorf("Load() with a %d-character token: %v", config.MinAdminTokenLen, err)
	}
}

func TestLoadErrorsNeverContainSecrets(t *testing.T) {
	t.Setenv("RG_ADMIN_TOKEN", "hunter2-short")
	if _, err := config.Load(); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Load() error = %v, want an error that does not quote the token", err)
	}
}

func TestLoadErrorsNeverContainTheDatabaseURL(t *testing.T) {
	t.Setenv("RG_DATABASE_URL", "postgres://u:hunter2@db/app")
	t.Setenv("RG_DATABASE_MAX_CONNS", "0")
	t.Setenv("RG_HTTP_IDLE_TIMEOUT", "0s")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Load() error leaks the database password: %q", err)
	}
}

func TestLoadDatabase(t *testing.T) {
	t.Setenv("RG_DATABASE_URL", testDatabaseURL)
	// A broken serving setting must not stop the migrate subcommand.
	t.Setenv("RG_HTTP_IDLE_TIMEOUT", "0s")

	db, err := config.LoadDatabase()
	if err != nil {
		t.Fatalf("LoadDatabase(): %v", err)
	}
	if db.URL.Value() != testDatabaseURL {
		t.Errorf("URL = %q, want %q", db.URL.Value(), testDatabaseURL)
	}
	if db.MaxConns != 10 {
		t.Errorf("MaxConns = %d, want default 10", db.MaxConns)
	}
}

func TestLoadDatabaseValidates(t *testing.T) {
	t.Setenv("RG_DATABASE_URL", testDatabaseURL)
	t.Setenv("RG_DATABASE_MAX_CONNS", "-1")

	if _, err := config.LoadDatabase(); err == nil {
		t.Fatal("LoadDatabase() with RG_DATABASE_MAX_CONNS=-1: want error, got nil")
	}
}
