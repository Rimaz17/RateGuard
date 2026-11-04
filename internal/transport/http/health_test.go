package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

type readyBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func ok(context.Context) error { return nil }

func failing(msg string) func(context.Context) error {
	return func(context.Context) error { return errors.New(msg) }
}

// blocking waits for its context, as a hung dependency would.
func blocking(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func getReadyz(t *testing.T, logs *bytes.Buffer, timeout time.Duration, checks ...httptransport.ReadinessCheck) (*httptest.ResponseRecorder, readyBody) {
	t.Helper()
	h := httptransport.NewRouter(httptransport.Deps{
		Logger:           slog.New(slog.NewJSONHandler(logs, nil)),
		Readiness:        checks,
		ReadinessTimeout: timeout,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))

	var body readyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		checks     []httptransport.ReadinessCheck
		wantStatus int
		wantBody   string
		wantChecks map[string]string
	}{
		{
			name:       "all dependencies up",
			checks:     []httptransport.ReadinessCheck{{Name: "postgres", Check: ok}, {Name: "redis", Check: ok}},
			wantStatus: http.StatusOK,
			wantBody:   "ready",
			wantChecks: map[string]string{"postgres": "ok", "redis": "ok"},
		},
		{
			name:       "postgres down",
			checks:     []httptransport.ReadinessCheck{{Name: "postgres", Check: failing("dial tcp: refused")}, {Name: "redis", Check: ok}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "not ready",
			wantChecks: map[string]string{"postgres": "unavailable", "redis": "ok"},
		},
		{
			name:       "both down",
			checks:     []httptransport.ReadinessCheck{{Name: "postgres", Check: failing("a")}, {Name: "redis", Check: failing("b")}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "not ready",
			wantChecks: map[string]string{"postgres": "unavailable", "redis": "unavailable"},
		},
		{
			name:       "no dependencies configured",
			wantStatus: http.StatusOK,
			wantBody:   "ready",
			wantChecks: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec, body := getReadyz(t, &logs, time.Second, tt.checks...)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if body.Status != tt.wantBody {
				t.Errorf("body status = %q, want %q", body.Status, tt.wantBody)
			}
			if len(body.Checks) != len(tt.wantChecks) {
				t.Errorf("checks = %v, want %v", body.Checks, tt.wantChecks)
			}
			for name, want := range tt.wantChecks {
				if body.Checks[name] != want {
					t.Errorf("checks[%s] = %q, want %q", name, body.Checks[name], want)
				}
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestReadyzKeepsErrorsOutOfTheBody(t *testing.T) {
	var logs bytes.Buffer
	rec, _ := getReadyz(t, &logs, time.Second,
		httptransport.ReadinessCheck{Name: "postgres", Check: failing("password authentication failed for user rateguard")})

	if strings.Contains(rec.Body.String(), "password authentication") {
		t.Errorf("body leaks the dependency error: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "password authentication") || !strings.Contains(logs.String(), `"check":"postgres"`) {
		t.Errorf("log should carry the check name and error, got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("a failed check should log at WARN, got: %s", logs.String())
	}
}

func TestReadyzTimesOutHungDependencies(t *testing.T) {
	var logs bytes.Buffer
	start := time.Now()
	rec, body := getReadyz(t, &logs, 150*time.Millisecond,
		httptransport.ReadinessCheck{Name: "postgres", Check: blocking},
		httptransport.ReadinessCheck{Name: "redis", Check: blocking},
		httptransport.ReadinessCheck{Name: "queue", Check: blocking},
		httptransport.ReadinessCheck{Name: "other", Check: ok},
	)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if body.Checks["postgres"] != "unavailable" || body.Checks["redis"] != "unavailable" || body.Checks["other"] != "ok" {
		t.Errorf("checks = %v, want both hung dependencies unavailable and other ok", body.Checks)
	}
	// Checks run concurrently, so three hung ones cost one 150ms timeout;
	// run one after another they would take 450ms.
	if elapsed > 400*time.Millisecond {
		t.Errorf("probe took %s, want about one 150ms timeout", elapsed)
	}
}
