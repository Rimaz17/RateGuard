package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with no environment: %v", err)
	}

	want := config.Config{
		HTTPAddr:              ":8080",
		HTTPReadHeaderTimeout: 5 * time.Second,
		HTTPReadTimeout:       10 * time.Second,
		HTTPWriteTimeout:      10 * time.Second,
		HTTPIdleTimeout:       120 * time.Second,
		ShutdownTimeout:       15 * time.Second,
		LogLevel:              slog.LevelInfo,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("RG_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("RG_HTTP_WRITE_TIMEOUT", "250ms")
	t.Setenv("RG_SHUTDOWN_TIMEOUT", "1m")
	t.Setenv("RG_LOG_LEVEL", "debug")

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
}

func TestLoadEmptyValueUsesDefault(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
