package httptransport_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(httptransport.NewRouter(logger))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if resp.Header.Get(httptransport.RequestIDHeader) == "" {
		t.Errorf("response is missing the %s header", httptransport.RequestIDHeader)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body status = %q, want %q", body["status"], "ok")
	}
}

func TestRoutingMisses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound},
		{"wrong method on healthz", http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	}
	srv := newTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tt.method, tt.path, err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}
