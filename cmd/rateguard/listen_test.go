package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// The service ships as a Linux image, and these tests depend on Unix socket
// file semantics that Windows reports differently.
func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket file semantics differ on Windows; run in the Linux test image")
	}
}

// socketPath returns a short path in a fresh directory: socket paths have
// a small length limit.
func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "rg.sock")
}

func TestListenUnixSetsTheMode(t *testing.T) {
	skipOnWindows(t)
	path := socketPath(t)
	ln, err := listenUnix(t.Context(), path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want a socket with 0600", fi.Mode())
	}
}

func TestListenUnixReplacesAStaleSocket(t *testing.T) {
	skipOnWindows(t)
	path := socketPath(t)
	// What a crash leaves behind: the file, with nobody listening.
	var lc net.ListenConfig
	old, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = old.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the stale socket should still exist: %v", err)
	}

	ln, err := listenUnix(t.Context(), path, 0o660)
	if err != nil {
		t.Fatalf("listenUnix over a stale socket: %v", err)
	}
	_ = ln.Close()
}

func TestListenUnixRefusesALiveSocket(t *testing.T) {
	skipOnWindows(t)
	path := socketPath(t)
	var lc net.ListenConfig
	live, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	go func() {
		for {
			c, err := live.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	if ln, err := listenUnix(t.Context(), path, 0o660); err == nil || !strings.Contains(err.Error(), "in use") {
		if ln != nil {
			_ = ln.Close()
		}
		t.Fatalf("listenUnix over a live socket: error = %v, want it refused", err)
	}
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatalf("the other process's socket stopped working: %v", err)
	}
	_ = c.Close()
}

func TestListenUnixNeverDeletesAnotherFile(t *testing.T) {
	skipOnWindows(t)
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(t.Context(), path, 0o660); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("listenUnix over a regular file: error = %v, want it refused", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "precious" {
		t.Errorf("the file was changed: %q, %v", b, err)
	}
}

// whoServed answers with whether the request came from a trusted peer, as
// ClientIP sees it.
var whoServed = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	ip, ok := httptransport.ClientIP(r, nil)
	if !ok {
		_, _ = io.WriteString(w, "no address")
		return
	}
	_, _ = io.WriteString(w, ip.String())
})

func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

func TestServeAllServesTCPAndTheSocketThenCleansUp(t *testing.T) {
	skipOnWindows(t)
	path := socketPath(t)
	tcp, err := listenTCP(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sock, err := listenUnix(t.Context(), path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	sockSrv := &http.Server{Handler: whoServed, ReadHeaderTimeout: time.Second, ConnContext: httptransport.TrustPeer}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serveAll(ctx, slog.New(slog.DiscardHandler), 5*time.Second, []endpoint{
			{ln: tcp, srv: &http.Server{Handler: whoServed, ReadHeaderTimeout: time.Second}},
			{ln: sock, srv: sockSrv},
		})
	}()

	// Over TCP the peer is the loopback address and its header is ignored;
	// over the socket the trusted peer's header names the caller.
	if got := get(t, http.DefaultClient, "http://"+tcp.Addr().String()+"/"); got != "127.0.0.1" {
		t.Errorf("over TCP the caller is %q, want 127.0.0.1", got)
	}
	if got := get(t, unixClient(path), "http://rateguard/"); got != "198.51.100.9" {
		t.Errorf("over the socket the caller is %q, want the forwarded 198.51.100.9", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveAll after shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveAll did not return after its context ended")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the socket file is still there after shutdown: %v", err)
	}
}

func TestServeAllStopsEverythingWhenOneEndpointFails(t *testing.T) {
	healthy, err := listenTCP(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	broken, err := listenTCP(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = broken.Close() // Serve on it fails at once

	done := make(chan error, 1)
	go func() {
		done <- serveAll(t.Context(), slog.New(slog.DiscardHandler), 5*time.Second, []endpoint{
			{ln: healthy, srv: &http.Server{Handler: whoServed, ReadHeaderTimeout: time.Second}},
			{ln: broken, srv: &http.Server{Handler: whoServed, ReadHeaderTimeout: time.Second}},
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serveAll returned nil although an endpoint failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveAll kept running after an endpoint failed")
	}
	var d net.Dialer
	if c, err := d.DialContext(t.Context(), "tcp", healthy.Addr().String()); err == nil {
		_ = c.Close()
		t.Error("the healthy endpoint is still accepting connections after serveAll returned")
	}
}

func TestListenTCPFailsFastOnATakenPort(t *testing.T) {
	first, err := listenTCP(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if ln, err := listenTCP(t.Context(), first.Addr().String()); err == nil {
		_ = ln.Close()
		t.Error("listening on a taken port succeeded, want an error at startup")
	}
}
