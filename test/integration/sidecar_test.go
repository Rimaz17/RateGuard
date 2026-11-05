//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// serveOnSocket serves the node's router on a Unix socket as main does with
// RG_UNIX_SOCKET, and returns a client that talks to it.
func serveOnSocket(t *testing.T, n *node) *http.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rg.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("listening on %s: %v", path, err)
	}
	srv := &http.Server{
		Handler:           n.srv.Config.Handler,
		ReadHeaderTimeout: time.Second,
		ConnContext:       httptransport.TrustPeer,
	}
	go func() { _ = srv.Serve(ln) }() // returns ErrServerClosed at cleanup
	t.Cleanup(func() { _ = srv.Close() })
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

type sidecarReply struct {
	status int
	header http.Header
	body   map[string]any
}

func viaSocket(t *testing.T, c *http.Client, endpoint, key, forwardedFor string) sidecarReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://rateguard"+endpoint,
		strings.NewReader(`{"method":"GET","path":"/api/products"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s over the socket: %v", endpoint, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
	return sidecarReply{status: resp.StatusCode, header: resp.Header, body: body}
}

// TestSidecarSocketDecides runs the real decision path over a Unix socket
// (ADR 0017): keyed callers by key, keyless ones by the address the
// application forwards, and nobody without either.
func TestSidecarSocketDecides(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	n := newNode(t, pool, nodeOptions{redisURL: redisURL, instances: 1, timeout: time.Second, failures: 5, cooldown: time.Second})
	adminJSON(t, n.srv, http.MethodPost, "/v1/policies", `{"name":"Sidecar","capacity":2,"window_seconds":3600,"default":true}`, http.StatusCreated)
	_, key := registerOn(t, n.srv, "Customer App", "")
	sock := serveOnSocket(t, n)

	// Two forwarded callers get a bucket each.
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		for i, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
			if r := viaSocket(t, sock, "/v1/check", "", ip); r.status != want {
				t.Fatalf("caller %s request %d = %d %v, want %d", ip, i+1, r.status, r.body, want)
			}
		}
	}
	// A key is still a key over the socket.
	if r := viaSocket(t, sock, "/v1/check", key, "198.51.100.1"); r.status != http.StatusOK || r.header.Get("X-RateLimit-Remaining") != "1" {
		t.Errorf("keyed request = %d %v, want 200 from the client's own bucket", r.status, r.body)
	}
	// With neither, there is nobody to count it against.
	if r := viaSocket(t, sock, "/v1/check", "", ""); r.status != http.StatusUnauthorized {
		t.Errorf("request with no key and no X-Forwarded-For = %d, want 401", r.status)
	}
}

// TestPeekTakesNothingOnRealRedis checks ADR 0016 end to end: peeks report
// the bucket without spending it, and a refused one still answers 200.
func TestPeekTakesNothingOnRealRedis(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	n := newNode(t, pool, nodeOptions{redisURL: redisURL, instances: 1, timeout: time.Second, failures: 5, cooldown: time.Second})
	adminJSON(t, n.srv, http.MethodPost, "/v1/policies", `{"name":"Peek","capacity":2,"window_seconds":3600,"default":true}`, http.StatusCreated)
	_, key := registerOn(t, n.srv, "Customer App", "")
	sock := serveOnSocket(t, n)

	for range 3 {
		if r := viaSocket(t, sock, "/v1/peek", key, ""); r.status != http.StatusOK || r.body["remaining"] != 2.0 {
			t.Fatalf("peek of a fresh bucket = %d %v, want 200 with 2 remaining every time", r.status, r.body)
		}
	}
	viaSocket(t, sock, "/v1/check", key, "")
	viaSocket(t, sock, "/v1/check", key, "")
	r := viaSocket(t, sock, "/v1/peek", key, "")
	if r.status != http.StatusOK || r.body["allowed"] != false || r.body["retry_after"] == nil || r.header.Get("Retry-After") != "" {
		t.Errorf("peek of an empty bucket = %d %v (Retry-After %q), want 200, allowed false, retry_after, no Retry-After header",
			r.status, r.body, r.header.Get("Retry-After"))
	}
}
