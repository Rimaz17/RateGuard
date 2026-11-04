//go:build integration

package integration

import (
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
)

// tcpProxy forwards one local address to a target, and can be killed and
// restored on the same address. Killing it is, to its clients, Redis
// dying: open connections drop and new ones are refused. Restarting the
// Redis container instead would give it a new port.
type tcpProxy struct {
	ctx    context.Context // the test's; ends dials and listens with it
	target string
	addr   string

	mu    sync.Mutex
	ln    net.Listener
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// startRedisProxy starts a proxy to the Redis at redisURL and returns it
// with a URL that goes through it.
func startRedisProxy(t *testing.T, redisURL string) (*tcpProxy, string) {
	t.Helper()
	u, err := url.Parse(redisURL)
	if err != nil {
		t.Fatalf("parsing %s: %v", redisURL, err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	p := &tcpProxy{ctx: t.Context(), target: u.Host, addr: ln.Addr().String(), conns: map[net.Conn]struct{}{}}
	p.serve(ln)
	t.Cleanup(func() {
		p.Kill()
		p.wg.Wait()
	})
	u.Host = p.addr
	return p, u.String()
}

func (p *tcpProxy) serve(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	p.wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // closed by Kill
			}
			p.wg.Go(func() { p.pipe(c) })
		}
	})
}

func (p *tcpProxy) pipe(client net.Conn) {
	var d net.Dialer
	upstream, err := d.DialContext(p.ctx, "tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	if !p.track(client, upstream) {
		return
	}
	var wg sync.WaitGroup
	copyAndClose := func(dst, src net.Conn) {
		// Copying ends when either side drops or Kill closes them; either
		// way, closing both ends is all there is to do.
		_, _ = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
	}
	wg.Go(func() { copyAndClose(upstream, client) })
	wg.Go(func() { copyAndClose(client, upstream) })
	wg.Wait()
	p.untrack(client, upstream)
}

// track registers a connection pair, or closes it if the proxy was killed
// while it was being set up.
func (p *tcpProxy) track(conns ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		for _, c := range conns {
			_ = c.Close()
		}
		return false
	}
	for _, c := range conns {
		p.conns[c] = struct{}{}
	}
	return true
}

func (p *tcpProxy) untrack(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range conns {
		delete(p.conns, c)
	}
}

// Kill stops accepting connections and drops every open one.
func (p *tcpProxy) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
	for c := range p.conns {
		_ = c.Close()
	}
	clear(p.conns)
}

// Restore accepts connections again, on the same address.
func (p *tcpProxy) Restore(t *testing.T) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", p.addr)
	if err != nil {
		t.Fatalf("re-listening on %s: %v", p.addr, err)
	}
	p.serve(ln)
}
