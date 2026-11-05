package httptransport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/limiter"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// stubDecider answers with a fixed decision and records what it was asked.
// The real decision path, on real Redis, is covered in test/integration.
type stubDecider struct {
	mu       sync.Mutex
	decision limiter.Decision
	err      error
	asked    []string
	ids      []domain.Identity
}

func (s *stubDecider) Check(_ context.Context, id domain.Identity, method, path string) (limiter.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, method+" "+path)
	s.ids = append(s.ids, id)
	return s.decision, s.err
}

type checkServer struct {
	srv     *httptest.Server
	decider *stubDecider
	logs    *bytes.Buffer
}

func newCheckServer(t *testing.T, decider *stubDecider) *checkServer {
	t.Helper()
	logs := &bytes.Buffer{}
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:        slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Decider:       decider,
		Authenticator: &stubAuth{valid: validKey},
	}))
	t.Cleanup(srv.Close)
	return &checkServer{srv: srv, decider: decider, logs: logs}
}

// reply is what a decision request got back.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (c *checkServer) post(t *testing.T, key, body string) (reply, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.srv.URL+"/v1/check", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/check: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, b
}

func decodeObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
	return m
}

const productsBody = `{"method":"GET","path":"/api/products"}`

func TestCheckAllowed(t *testing.T) {
	c := newCheckServer(t, &stubDecider{decision: limiter.Decision{
		Allowed: true, Limit: 100, Remaining: 72, ResetAfter: 17*time.Second + time.Millisecond,
	}})
	resp, body := c.post(t, validKey, productsBody)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", resp.StatusCode, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"allowed":true,"limit":100,"remaining":72,"reset_after":18}` {
		t.Errorf("body = %s, want the overview's allowed shape with reset_after rounded up", got)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if c.decider.asked[0] != "GET /api/products" || c.decider.ids[0].ClientID != clientA {
		t.Errorf("decider was asked %v for %+v, want GET /api/products for the keyed client", c.decider.asked, c.decider.ids[0])
	}
}

func TestCheckRefused(t *testing.T) {
	c := newCheckServer(t, &stubDecider{decision: limiter.Decision{
		Allowed: false, Limit: 100, Remaining: 0, ResetAfter: 11500 * time.Millisecond, RetryAfter: 11500 * time.Millisecond,
	}})
	resp, body := c.post(t, validKey, productsBody)

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d %s, want 429", resp.StatusCode, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"allowed":false,"limit":100,"remaining":0,"reset_after":12,"retry_after":12}` {
		t.Errorf("body = %s, want the overview's rejected shape", got)
	}
}

func TestCheckSendsTheRateLimitHeaders(t *testing.T) {
	tests := []struct {
		name     string
		decision limiter.Decision
		status   int
		want     map[string]string
	}{
		{
			name:     "allowed",
			decision: limiter.Decision{Allowed: true, Limit: 100, Remaining: 72, ResetAfter: 17*time.Second + time.Millisecond},
			status:   http.StatusOK,
			want:     map[string]string{"X-RateLimit-Limit": "100", "X-RateLimit-Remaining": "72", "X-RateLimit-Reset": "18", "Retry-After": ""},
		},
		{
			name:     "refused",
			decision: limiter.Decision{Allowed: false, Limit: 100, ResetAfter: 11500 * time.Millisecond, RetryAfter: 400 * time.Millisecond},
			status:   http.StatusTooManyRequests,
			want:     map[string]string{"X-RateLimit-Limit": "100", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "12", "Retry-After": "1"},
		},
		{
			name:     "full bucket resets in zero",
			decision: limiter.Decision{Allowed: true, Limit: 5, Remaining: 5},
			status:   http.StatusOK,
			want:     map[string]string{"X-RateLimit-Limit": "5", "X-RateLimit-Remaining": "5", "X-RateLimit-Reset": "0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCheckServer(t, &stubDecider{decision: tt.decision})
			resp, body := c.post(t, validKey, productsBody)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d %s, want %d", resp.StatusCode, body, tt.status)
			}
			for h, want := range tt.want {
				if got := resp.Header.Get(h); got != want {
					t.Errorf("%s = %q, want %q", h, got, want)
				}
			}
			// The headers and the body must never disagree (ADR 0016).
			m := decodeObject(t, body)
			if fmt.Sprint(m["reset_after"]) != resp.Header.Get("X-RateLimit-Reset") ||
				fmt.Sprint(m["remaining"]) != resp.Header.Get("X-RateLimit-Remaining") {
				t.Errorf("headers %v disagree with body %s", resp.Header, body)
			}
			if r, ok := m["retry_after"]; ok && fmt.Sprint(r) != resp.Header.Get("Retry-After") {
				t.Errorf("Retry-After %q disagrees with retry_after %v", resp.Header.Get("Retry-After"), r)
			}
		})
	}
}

func TestCheckErrorsCarryNoRateLimitHeaders(t *testing.T) {
	c := newCheckServer(t, &stubDecider{err: domain.ErrNoPolicy})
	resp, _ := c.post(t, validKey, productsBody)
	for _, h := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("a 403 carries %s = %q; there is no limit to report", h, v)
		}
	}
}

func TestCheckRetryAfterIsNeverZero(t *testing.T) {
	c := newCheckServer(t, &stubDecider{decision: limiter.Decision{Allowed: false, Limit: 5, RetryAfter: time.Nanosecond, ResetAfter: time.Nanosecond}})
	_, body := c.post(t, validKey, productsBody)
	if m := decodeObject(t, body); m["retry_after"] != 1.0 || m["reset_after"] != 1.0 {
		t.Errorf("body = %s, want a nanosecond rounded up to 1 second", body)
	}
}

func TestCheckIdentifiesKeylessCallersByIP(t *testing.T) {
	c := newCheckServer(t, &stubDecider{decision: limiter.Decision{Allowed: true, Limit: 1, Remaining: 0}})
	if resp, body := c.post(t, "", productsBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", resp.StatusCode, body)
	}
	if id := c.decider.ids[0]; id.Kind != domain.IdentityIP || !id.IP.IsLoopback() {
		t.Errorf("identity = %+v, want the loopback IP of the test connection", id)
	}
}

func TestCheckRejectsBadKeysBeforeDeciding(t *testing.T) {
	c := newCheckServer(t, &stubDecider{})
	resp, _ := c.post(t, "rg_cus_aaaaaaaaaaaaaaaa_"+strings.Repeat("z", 52), productsBody)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if len(c.decider.asked) != 0 {
		t.Error("a request with an invalid key reached the decider")
	}
}

func TestCheckNoPolicyIsForbidden(t *testing.T) {
	c := newCheckServer(t, &stubDecider{err: domain.ErrNoPolicy})
	resp, body := c.post(t, validKey, productsBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d %s, want 403: no policy is a refusal, never unlimited", resp.StatusCode, body)
	}
	if m := decodeObject(t, body); m["error"] != "no rate-limit policy applies to this caller" || m["request_id"] == "" {
		t.Errorf("body = %s, want the standard error shape", body)
	}
}

func TestCheckInternalErrorsLeakNothing(t *testing.T) {
	c := newCheckServer(t, &stubDecider{err: errors.New("redis: script error at line 7")})
	resp, body := c.post(t, validKey, productsBody)
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(string(body), "redis") {
		t.Errorf("status = %d, body = %s; want a generic 500", resp.StatusCode, body)
	}
	if !strings.Contains(c.logs.String(), "script error") {
		t.Error("the internal error was not logged")
	}
}

func TestCheckValidatesTheRequest(t *testing.T) {
	tests := []struct {
		name, body string
		status     int
		message    string
	}{
		{"no method", `{"path":"/x"}`, 400, "method is required"},
		{"method with a space", `{"method":"GE T","path":"/x"}`, 400, "method must be an HTTP method"},
		{"method too long", `{"method":"` + strings.Repeat("A", 33) + `","path":"/x"}`, 400, "at most 32"},
		{"no path", `{"method":"GET"}`, 400, "path is required"},
		{"relative path", `{"method":"GET","path":"api/x"}`, 400, "path must start with /"},
		{"query string", `{"method":"GET","path":"/api/login?next=/"}`, 400, "query string"},
		{"fragment", `{"method":"GET","path":"/api/login#x"}`, 400, "query string or fragment"},
		{"control character", `{"method":"GET","path":"/api/\u0000login"}`, 400, "control characters"},
		{"path too long", `{"method":"GET","path":"/` + strings.Repeat("a", 2048) + `"}`, 400, "at most 2048 bytes"},
		{"unknown field", `{"method":"GET","path":"/x","cost":5}`, 400, `unknown field \"cost\"`}, // as escaped in the JSON body
		{"body over 4 KiB", `{"method":"GET","path":"/` + strings.Repeat("a", 5000) + `"}`, 413, "at most 4096 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCheckServer(t, &stubDecider{})
			resp, body := c.post(t, validKey, tt.body)
			if resp.StatusCode != tt.status || !strings.Contains(string(body), tt.message) {
				t.Errorf("status = %d %s, want %d containing %q", resp.StatusCode, body, tt.status, tt.message)
			}
			if len(c.decider.asked) != 0 {
				t.Errorf("an invalid request reached the decider: %v", c.decider.asked)
			}
		})
	}
}

func TestCheckAcceptsAnyMethodToken(t *testing.T) {
	c := newCheckServer(t, &stubDecider{decision: limiter.Decision{Allowed: true, Limit: 1}})
	for _, m := range []string{"get", "PATCH", "PROPFIND", "M-SEARCH"} {
		if resp, body := c.post(t, validKey, `{"method":"`+m+`","path":"/"}`); resp.StatusCode != http.StatusOK {
			t.Errorf("method %q: status = %d %s, want 200", m, resp.StatusCode, body)
		}
	}
}

func TestCheckLogsNothingForDecisions(t *testing.T) {
	for name, d := range map[string]limiter.Decision{
		"allowed": {Allowed: true, Limit: 10, Remaining: 9},
		"refused": {Allowed: false, Limit: 10, RetryAfter: time.Second},
	} {
		c := newCheckServer(t, &stubDecider{decision: d})
		c.post(t, validKey, productsBody)
		if c.logs.Len() != 0 {
			t.Errorf("%s decision logged %q; the hot path must not log (ADR 0004)", name, c.logs.String())
		}
	}
}

func TestCheckIsPostOnly(t *testing.T) {
	c := newCheckServer(t, &stubDecider{})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, c.srv.URL+"/v1/check", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /v1/check = %d, want 405", resp.StatusCode)
	}
}
