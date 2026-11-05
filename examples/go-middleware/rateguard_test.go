package rateguard_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rateguard "github.com/Rimaz17/RateGuard/examples/go-middleware"
)

// fakeRateGuard answers /v1/check with a fixed status and body, and records
// what it was sent. The real service is exercised by TestE2E.
type fakeRateGuard struct {
	srv    *httptest.Server
	status int
	body   string
	delay  time.Duration
	conns  atomic.Int32

	mu       sync.Mutex
	requests []seen
}

type seen struct {
	method, path, auth, xff string
	body                    map[string]string
}

func newFake(t *testing.T, status int, body string) *fakeRateGuard {
	t.Helper()
	f := &fakeRateGuard{status: status, body: body}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			f.conns.Add(1)
		}
	}
	f.srv.Start()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRateGuard) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, seen{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Forwarded-For"), body})
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.body)
}

func (f *fakeRateGuard) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("RateGuard was never asked")
	}
	return f.requests[len(f.requests)-1]
}

// app is the protected application: it counts what reaches it.
type app struct{ served atomic.Int32 }

func (a *app) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	a.served.Add(1)
	_, _ = io.WriteString(w, "hello")
}

func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func newRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	r.RemoteAddr = "203.0.113.7:51000"
	return r
}

func mustNew(t *testing.T, addr string, opts ...rateguard.Option) *rateguard.Client {
	t.Helper()
	c, err := rateguard.New(addr, opts...)
	if err != nil {
		t.Fatalf("New(%q): %v", addr, err)
	}
	return c
}

const allowed = `{"allowed":true,"limit":100,"remaining":72,"reset_after":18}`

func TestMiddlewareServesAllowedRequestsWithHeaders(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	a := &app{}
	r := newRequest(t, "/api/products")
	r.Header.Set("Authorization", "Bearer rg_cus_key")

	rec := serve(t, mustNew(t, f.srv.URL).Middleware(a), r)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello" || a.served.Load() != 1 {
		t.Fatalf("response = %d %q, served %d; want the app's 200", rec.Code, rec.Body, a.served.Load())
	}
	for h, want := range map[string]string{"X-RateLimit-Limit": "100", "X-RateLimit-Remaining": "72", "X-RateLimit-Reset": "18", "Retry-After": ""} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	got := f.last(t)
	if got.method != http.MethodPost || got.path != "/v1/check" || got.body["method"] != "GET" || got.body["path"] != "/api/products" {
		t.Errorf("RateGuard was asked %+v, want POST /v1/check about GET /api/products", got)
	}
	if got.auth != "Bearer rg_cus_key" || got.xff != "203.0.113.7" {
		t.Errorf("forwarded Authorization %q and X-Forwarded-For %q, want the caller's key and address", got.auth, got.xff)
	}
}

func TestMiddlewareRefusesWithRetryAfter(t *testing.T) {
	f := newFake(t, http.StatusTooManyRequests, `{"allowed":false,"limit":100,"remaining":0,"reset_after":12,"retry_after":7}`)
	a := &app{}
	rec := serve(t, mustNew(t, f.srv.URL).Middleware(a), newRequest(t, "/api/products"))
	if rec.Code != http.StatusTooManyRequests || a.served.Load() != 0 {
		t.Fatalf("response = %d, served %d; want 429 and the app untouched", rec.Code, a.served.Load())
	}
	for h, want := range map[string]string{"X-RateLimit-Limit": "100", "X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "12", "Retry-After": "7"} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

func TestMiddlewarePassesOnRefusalsOfTheCaller(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newFake(t, status, `{"error":"invalid API key","request_id":"abc"}`)
		a := &app{}
		var reported error
		c := mustNew(t, f.srv.URL, rateguard.WithFailOpen(), rateguard.WithErrorHandler(func(_ *http.Request, err error) { reported = err }))
		rec := serve(t, c.Middleware(a), newRequest(t, "/"))
		if rec.Code != status || a.served.Load() != 0 || !strings.Contains(rec.Body.String(), "invalid API key") {
			t.Errorf("RateGuard said %d: response = %d %q, served %d; want the refusal passed on even when failing open", status, rec.Code, rec.Body, a.served.Load())
		}
		if reported != nil {
			t.Errorf("RateGuard said %d: reported %v as an error; it is an answer", status, reported)
		}
	}
}

func TestMiddlewareFailsClosedByDefault(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	addr := f.srv.URL
	f.srv.Close() // RateGuard is gone

	a := &app{}
	var reported error
	c := mustNew(t, addr, rateguard.WithErrorHandler(func(_ *http.Request, err error) { reported = err }))
	rec := serve(t, c.Middleware(a), newRequest(t, "/"))
	if rec.Code != http.StatusServiceUnavailable || a.served.Load() != 0 {
		t.Errorf("response = %d, served %d; want 503 and the app untouched", rec.Code, a.served.Load())
	}
	if reported == nil {
		t.Error("the error handler was not told")
	}
}

func TestMiddlewareCanFailOpen(t *testing.T) {
	for name, f := range map[string]*fakeRateGuard{
		"server error": newFake(t, http.StatusInternalServerError, `{"error":"internal server error","request_id":"x"}`),
		"bad request":  newFake(t, http.StatusBadRequest, `{"error":"path is required","request_id":"x"}`),
	} {
		a := &app{}
		rec := serve(t, mustNew(t, f.srv.URL, rateguard.WithFailOpen()).Middleware(a), newRequest(t, "/"))
		if rec.Code != http.StatusOK || a.served.Load() != 1 {
			t.Errorf("%s with WithFailOpen: response = %d, served %d; want the app's 200", name, rec.Code, a.served.Load())
		}
	}
}

func TestCheckTimesOut(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	f.delay = 5 * time.Second
	c := mustNew(t, f.srv.URL, rateguard.WithTimeout(50*time.Millisecond))

	start := time.Now()
	_, err := c.Check(t.Context(), newRequest(t, "/"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Check against a hung RateGuard: error = %v, want a deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Check took %v, want about the 50ms timeout", elapsed)
	}
}

func TestCheckReportsStatusErrors(t *testing.T) {
	f := newFake(t, http.StatusForbidden, `{"error":"no rate-limit policy applies to this caller","request_id":"req-1"}`)
	_, err := mustNew(t, f.srv.URL).Check(t.Context(), newRequest(t, "/"))
	var se *rateguard.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusForbidden || se.RequestID != "req-1" || !strings.Contains(se.Message, "no rate-limit policy") {
		t.Errorf("error = %#v, want a StatusError with RateGuard's message and request ID", err)
	}
}

func TestCheckRefusesAContradictoryAnswer(t *testing.T) {
	f := newFake(t, http.StatusOK, `{"allowed":false,"limit":1,"remaining":0,"reset_after":1,"retry_after":1}`)
	if _, err := mustNew(t, f.srv.URL).Check(t.Context(), newRequest(t, "/")); err == nil {
		t.Error("a 200 saying allowed false was accepted")
	}
}

func TestCheckForwardsTheRoutedPath(t *testing.T) {
	tests := []struct {
		name, target, want string
	}{
		{"plain", "/api/products", "/api/products"},
		{"the query string stays behind", "/api/login?next=/", "/api/login"},
		// The app routes on the decoded path, so RateGuard must see it too,
		// or /api/%6Cogin would reach /api/login without its rule.
		{"encoded letters are decoded", "/api/%6Cogin", "/api/login"},
		{"an encoded ? is encoded again", "/api/login%3Fx", "/api/login%3Fx"},
		{"an encoded # is encoded again", "/a%23b", "/a%23b"},
		{"control characters are encoded", "/a%0Ab", "/a%0Ab"},
	}
	f := newFake(t, http.StatusOK, allowed)
	c := mustNew(t, f.srv.URL)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := c.Check(t.Context(), newRequest(t, tt.target)); err != nil {
				t.Fatal(err)
			}
			if got := f.last(t).body["path"]; got != tt.want {
				t.Errorf("forwarded path = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckTruncatesOverlongPaths(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	if _, err := mustNew(t, f.srv.URL).Check(t.Context(), newRequest(t, "/"+strings.Repeat("a", 5000))); err != nil {
		t.Fatal(err)
	}
	if n := len(f.last(t).body["path"]); n != 2048 {
		t.Errorf("forwarded path is %d bytes, want RateGuard's maximum of 2048", n)
	}
}

func TestCheckAppendsToForwardedFor(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	r := newRequest(t, "/")
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	r.RemoteAddr = "10.0.0.2:5555" // this app's own load balancer
	if _, err := mustNew(t, f.srv.URL).Check(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).xff; got != "198.51.100.9, 10.0.0.2" {
		t.Errorf("X-Forwarded-For = %q, want the incoming chain with the peer appended", got)
	}
}

func TestClientReusesConnections(t *testing.T) {
	f := newFake(t, http.StatusOK, allowed)
	c := mustNew(t, f.srv.URL)
	for range 50 {
		if _, err := c.Check(t.Context(), newRequest(t, "/")); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.conns.Load(); n != 1 {
		t.Errorf("50 sequential checks opened %d connections, want 1 kept alive", n)
	}
}

func TestClientTalksToASidecarSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket files behave differently on Windows")
	}
	path := filepath.Join(t.TempDir(), "rg.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/check" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, allowed)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	d, err := mustNew(t, "unix://"+path).Check(t.Context(), newRequest(t, "/"))
	if err != nil || !d.Allowed || d.Remaining != 72 {
		t.Errorf("Check over the socket = %+v, %v; want the allowed decision", d, err)
	}
}

func TestNewRejectsBadAddresses(t *testing.T) {
	for _, addr := range []string{"", "rateguard:8080", "ftp://rateguard", "http://", "unix://relative/path", "unix://"} {
		if _, err := rateguard.New(addr); err == nil {
			t.Errorf("New(%q) succeeded, want an error", addr)
		}
	}
}
