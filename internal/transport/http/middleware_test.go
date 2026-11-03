package httptransport_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRequestID(t *testing.T) {
	tests := []struct {
		name    string
		inbound string
		reuse   bool
	}{
		{"absent is generated", "", false},
		{"valid inbound is reused", "edge-proxy_42.a", true},
		{"64 chars is reused", strings.Repeat("a", 64), true},
		{"65 chars is replaced", strings.Repeat("a", 65), false},
		{"unsafe characters are replaced", "abc\" injected=1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			h := httptransport.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = httptransport.RequestIDFromContext(r.Context())
			}))

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.inbound != "" {
				req.Header.Set(httptransport.RequestIDHeader, tt.inbound)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get(httptransport.RequestIDHeader)
			if got != seen {
				t.Errorf("response header %q != context value %q", got, seen)
			}
			if tt.reuse {
				if got != tt.inbound {
					t.Errorf("request ID = %q, want inbound %q", got, tt.inbound)
				}
				return
			}
			if !generatedID.MatchString(got) {
				t.Errorf("request ID = %q, want 32 lowercase hex chars", got)
			}
		})
	}
}

func TestRequestIDsAreUnique(t *testing.T) {
	h := httptransport.RequestID(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	seen := make(map[string]bool)
	for range 1000 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		id := rec.Header().Get(httptransport.RequestIDHeader)
		if seen[id] {
			t.Fatalf("duplicate request ID %q", id)
		}
		seen[id] = true
	}
}

func TestRequestIDFromContextOutsideRequest(t *testing.T) {
	if got := httptransport.RequestIDFromContext(t.Context()); got != "" {
		t.Errorf("RequestIDFromContext(bare context) = %q, want empty", got)
	}
}

func TestRecovererHidesPanicFromClient(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	h := httptransport.RequestID(httptransport.Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") {
		t.Errorf("response body leaks the panic value: %s", rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	wantID := rec.Header().Get(httptransport.RequestIDHeader)
	if body["request_id"] != wantID {
		t.Errorf("body request_id = %q, want %q", body["request_id"], wantID)
	}
	if !strings.Contains(logs.String(), "secret internal detail") || !strings.Contains(logs.String(), wantID) {
		t.Errorf("log should carry the panic value and request ID, got: %s", logs.String())
	}
}

func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	h := httptransport.Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		if rec, _ := recover().(error); !errors.Is(rec, http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler to propagate", rec)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
}
