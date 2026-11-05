package config_test

import (
	"log/slog"
	"net/netip"
	"path/filepath"
	"reflect"
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
		InstanceCount:  1,
		UnixSocketMode: 0o660,
		Redis: config.Redis{
			URL:             "redis://localhost:6379/0",
			Timeout:         100 * time.Millisecond,
			FailureMode:     config.FailureModeLocal,
			BreakerFailures: 5,
			BreakerCooldown: 5 * time.Second,
		},
		Usage: config.Usage{
			FlushInterval:  time.Second,
			RollupInterval: 10 * time.Second,
		},
	}
	if !reflect.DeepEqual(cfg, want) {
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
	t.Setenv("RG_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.7/32,,fd00::1/8 ")
	t.Setenv("RG_INSTANCE_COUNT", "3")
	t.Setenv("RG_REDIS_TIMEOUT", "40ms")
	t.Setenv("RG_REDIS_FAILURE_MODE", "closed")
	t.Setenv("RG_REDIS_BREAKER_FAILURES", "2")
	t.Setenv("RG_REDIS_BREAKER_COOLDOWN", "750ms")
	t.Setenv("RG_USAGE_FLUSH_INTERVAL", "250ms")
	t.Setenv("RG_USAGE_ROLLUP_INTERVAL", "1m")

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
	wantProxies := config.Prefixes{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.1.7/32"),
		netip.MustParsePrefix("fd00::/8"),
	}
	if !reflect.DeepEqual(cfg.TrustedProxies, wantProxies) {
		t.Errorf("TrustedProxies = %v, want %v", cfg.TrustedProxies, wantProxies)
	}
	if cfg.InstanceCount != 3 {
		t.Errorf("InstanceCount = %d, want 3", cfg.InstanceCount)
	}
	r := cfg.Redis
	if r.Timeout != 40*time.Millisecond || r.FailureMode != config.FailureModeClosed || r.BreakerFailures != 2 || r.BreakerCooldown != 750*time.Millisecond {
		t.Errorf("Redis failure settings = %s, %s, %d, %s; want 40ms, closed, 2, 750ms", r.Timeout, r.FailureMode, r.BreakerFailures, r.BreakerCooldown)
	}
	if u := cfg.Usage; u.FlushInterval != 250*time.Millisecond || u.RollupInterval != time.Minute {
		t.Errorf("Usage intervals = %s, %s; want 250ms, 1m", u.FlushInterval, u.RollupInterval)
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
		{"trusted proxy without mask", "RG_TRUSTED_PROXIES", "10.0.0.1", "parsing environment"},
		{"trusted proxy not an address", "RG_TRUSTED_PROXIES", "lb.internal/32", "parsing environment"},
		{"zero instance count", "RG_INSTANCE_COUNT", "0", "RG_INSTANCE_COUNT must be at least 1, got 0"},
		{"negative instance count", "RG_INSTANCE_COUNT", "-3", "RG_INSTANCE_COUNT must be at least 1, got -3"},
		{"zero redis timeout", "RG_REDIS_TIMEOUT", "0s", "RG_REDIS_TIMEOUT must be positive"},
		{"unknown failure mode", "RG_REDIS_FAILURE_MODE", "unlimited", `RG_REDIS_FAILURE_MODE must be local, open or closed, got "unlimited"`},
		{"failure mode is case-sensitive", "RG_REDIS_FAILURE_MODE", "Local", "RG_REDIS_FAILURE_MODE must be local, open or closed"},
		{"zero breaker failures", "RG_REDIS_BREAKER_FAILURES", "0", "RG_REDIS_BREAKER_FAILURES must be at least 1"},
		{"negative breaker failures", "RG_REDIS_BREAKER_FAILURES", "-1", "parsing environment"},
		{"zero breaker cooldown", "RG_REDIS_BREAKER_COOLDOWN", "0s", "RG_REDIS_BREAKER_COOLDOWN must be positive"},
		{"relative socket path", "RG_UNIX_SOCKET", "run/rateguard.sock", "RG_UNIX_SOCKET must be an absolute path"},
		{"socket path too long", "RG_UNIX_SOCKET", "/" + strings.Repeat("s", 104), "RG_UNIX_SOCKET must be at most 104 bytes, got 105"},
		{"socket mode not octal", "RG_UNIX_SOCKET_MODE", "0698", "parsing environment"},
		{"socket mode beyond permissions", "RG_UNIX_SOCKET_MODE", "4777", "parsing environment"},
		{"socket mode in words", "RG_UNIX_SOCKET_MODE", "rw-rw----", "parsing environment"},
		{"zero usage flush interval", "RG_USAGE_FLUSH_INTERVAL", "0s", "RG_USAGE_FLUSH_INTERVAL must be positive"},
		{"negative usage rollup interval", "RG_USAGE_ROLLUP_INTERVAL", "-10s", "RG_USAGE_ROLLUP_INTERVAL must be positive"},
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

func TestLoadUnixSocket(t *testing.T) {
	setRequired(t)
	path, err := filepath.Abs("rateguard.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RG_UNIX_SOCKET", path)
	t.Setenv("RG_UNIX_SOCKET_MODE", "600")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.UnixSocket != path || cfg.UnixSocketMode.Perm() != 0o600 {
		t.Errorf("socket = %q mode %o, want %q mode 600", cfg.UnixSocket, cfg.UnixSocketMode.Perm(), path)
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
