package httptransport_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:           slog.New(slog.DiscardHandler),
		ReadinessTimeout: time.Second,
	}))
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
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestMetricsRouteServesTheGivenHandler(t *testing.T) {
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger: slog.New(slog.DiscardHandler),
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("rateguard_redis_breaker_state 0\n"))
		}),
	}))
	t.Cleanup(srv.Close)

	for method, want := range map[string]int{http.MethodGet: http.StatusOK, http.MethodPost: http.StatusMethodNotAllowed} {
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s /metrics: %v", method, err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s /metrics = %d, want %d", method, resp.StatusCode, want)
		}
	}
}

func TestMetricsRouteIsAbsentWithoutAHandler(t *testing.T) {
	srv := newTestServer(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /metrics without a handler = %d, want 404", resp.StatusCode)
	}
}

func TestRoutingMissesHaveTheStandardErrorBody(t *testing.T) {
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:        slog.New(slog.DiscardHandler),
		Decider:       &stubDecider{},
		Authenticator: &stubAuth{valid: validKey},
		Policies:      &stubPolicies{},
		AdminToken:    adminToken,
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		method, path string
		status       int
		allow        []string
	}{
		{http.MethodGet, "/nope", http.StatusNotFound, nil},
		{http.MethodGet, "/v1/check", http.StatusMethodNotAllowed, []string{"POST"}},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, []string{"GET"}},
		// Inside the admin sub-router, once its authentication has passed.
		// Without the token these are 401s, so the admin routes stay hidden.
		{http.MethodPut, "/v1/policies/" + policyFree, http.StatusMethodNotAllowed, []string{"GET", "PATCH", "DELETE"}},
		{http.MethodGet, "/v1/policies/" + policyFree + "/nope", http.StatusNotFound, nil},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), tt.method, srv.URL+tt.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+adminToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if body["error"] == "" || body["request_id"] != resp.Header.Get(httptransport.RequestIDHeader) {
				t.Errorf("body = %v, want the standard error with this request's ID", body)
			}
			if got := resp.Header.Values("Allow"); fmt.Sprint(got) != fmt.Sprint(tt.allow) && tt.allow != nil {
				t.Errorf("Allow = %v, want %v", got, tt.allow)
			}
		})
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
		{"wrong method on readyz", http.MethodDelete, "/readyz", http.StatusMethodNotAllowed},
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
