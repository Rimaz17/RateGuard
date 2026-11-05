package rateguard_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	rateguard "github.com/Rimaz17/RateGuard/examples/go-middleware"
)

// TestE2E protects a handler with the middleware against a real RateGuard.
// It runs when these are set, as CI does against the compose stack:
//
//	RATEGUARD_URL      where RateGuard is, e.g. http://127.0.0.1:8080
//	RATEGUARD_API_KEY  a key whose client has a fresh bucket
//	RATEGUARD_LIMIT    that client's capacity, refilling slowly
func TestE2E(t *testing.T) {
	addr, key := os.Getenv("RATEGUARD_URL"), os.Getenv("RATEGUARD_API_KEY")
	limit, err := strconv.Atoi(os.Getenv("RATEGUARD_LIMIT"))
	if addr == "" || key == "" || err != nil {
		t.Skip("set RATEGUARD_URL, RATEGUARD_API_KEY and RATEGUARD_LIMIT to run against a real RateGuard")
	}
	rg, err := rateguard.New(addr)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	protected := rg.Middleware(a)

	for i := range limit + 1 {
		r := newRequest(t, "/api/products")
		r.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, r)

		if i < limit {
			if rec.Code != http.StatusOK || rec.Header().Get("X-RateLimit-Remaining") != strconv.Itoa(limit-1-i) {
				t.Fatalf("request %d = %d (remaining %q), want 200 with %d remaining", i+1, rec.Code, rec.Header().Get("X-RateLimit-Remaining"), limit-1-i)
			}
			continue
		}
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("request %d = %d (Retry-After %q), want 429 with Retry-After", i+1, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	if n := a.served.Load(); int(n) != limit {
		t.Errorf("the app served %d requests, want exactly the limit of %d", n, limit)
	}

	// A made-up key is refused by RateGuard, and so by the middleware.
	r := newRequest(t, "/api/products")
	r.Header.Set("Authorization", "Bearer rg_xyz_aaaaaaaaaaaaaaaa_"+repeat('b', 52))
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a made-up key got %d, want 401", rec.Code)
	}
}

func repeat(b byte, n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = b
	}
	return string(s)
}
