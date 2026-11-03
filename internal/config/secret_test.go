package config_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/config"
)

func TestSecretNeverRendersItsValue(t *testing.T) {
	const value = "postgres://user:hunter2@db:5432/app"
	s := config.Secret(value)

	if got := s.Value(); got != value {
		t.Fatalf("Value() = %q, want %q", got, value)
	}

	wrapped := struct{ URL config.Secret }{s}
	jsonOut, err := json.Marshal(wrapped)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("config", slog.Any("url", s), slog.Any("wrapped", wrapped))

	renderings := map[string]string{
		"%v":       fmt.Sprintf("%v", s),
		"%q":       fmt.Sprintf("%q", s),
		"%#v":      fmt.Sprintf("%#v", s),
		"%+v (in)": fmt.Sprintf("%+v", wrapped),
		"json":     string(jsonOut),
		"slog":     logs.String(),
	}
	for name, out := range renderings {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s rendering leaks the secret: %s", name, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s rendering = %s, want it to say [REDACTED]", name, out)
		}
	}
}
