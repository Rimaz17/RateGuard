package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewRedisClient(t *testing.T) {
	rdb, err := newRedisClient("redis://:hunter2@cache:6380/3")
	if err != nil {
		t.Fatalf("newRedisClient: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	opts := rdb.Options()
	if opts.Addr != "cache:6380" || opts.DB != 3 || opts.Password != "hunter2" {
		t.Errorf("options = addr %q db %d, want cache:6380 db 3 with the password", opts.Addr, opts.DB)
	}
	if !opts.ContextTimeoutEnabled {
		t.Error("ContextTimeoutEnabled = false, want true so readiness timeouts bind")
	}
}

func TestNewRedisClientNeverRetries(t *testing.T) {
	// Even a URL asking for retries gets none: a retried bucket script can
	// take a token twice (ADR 0013). go-redis stores "disabled" as 0.
	for _, url := range []string{"redis://cache:6379/0", "redis://cache:6379/0?max_retries=5"} {
		rdb, err := newRedisClient(url)
		if err != nil {
			t.Fatalf("newRedisClient(%q): %v", url, err)
		}
		if n := rdb.Options().MaxRetries; n != 0 {
			t.Errorf("newRedisClient(%q): MaxRetries = %d, want 0 (disabled)", url, n)
		}
		_ = rdb.Close()
	}
}

func TestNewRedisClientInvalidURLDoesNotLeakPassword(t *testing.T) {
	for _, url := range []string{"http://:hunter2@cache:6379", "redis://:hunter2@cache:6379/notadb", "redis://:hunter2@[::1"} {
		_, err := newRedisClient(url)
		if err == nil {
			t.Errorf("newRedisClient(%q): want error, got nil", url)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("newRedisClient error leaks the password: %q", err)
		}
	}
}

func TestRedisLoggerWritesStructuredWarnings(t *testing.T) {
	var buf bytes.Buffer
	redisLogger{slog.New(slog.NewJSONHandler(&buf, nil))}.Printf(t.Context(), "failed to dial after %d attempts", 5)

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %q", buf.String())
	}
	if line["level"] != "WARN" || line["detail"] != "failed to dial after 5 attempts" {
		t.Errorf("log line = %v, want a WARN carrying the formatted detail", line)
	}
}
