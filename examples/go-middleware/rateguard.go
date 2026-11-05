// Package rateguard is a reference integration for RateGuard: an HTTP
// client for its decision endpoint, and a net/http middleware that asks it
// about every request and refuses those it refuses.
//
// It uses only the standard library, so adding it adds no dependencies.
// The client keeps a pool of keep-alive connections to RateGuard, because
// the cost of the extra hop is mostly the cost of opening one (ADR 0004),
// and it can talk to a sidecar over a Unix socket (ADR 0017):
//
//	rg, err := rateguard.New("http://rateguard:8080")    // central
//	rg, err := rateguard.New("unix:///run/rateguard.sock") // sidecar
//	...
//	http.ListenAndServe(":8000", rg.Middleware(mux))
package rateguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one decision. RateGuard answers in single-digit
// milliseconds, and itself gives Redis 100ms before degrading, so this
// leaves room for a slow moment without holding requests for long.
const DefaultTimeout = 250 * time.Millisecond

// Limits of RateGuard's decision request (ADR 0015).
const (
	maxPathLen      = 2048
	maxReplyBytes   = 64 << 10
	idleConnsToRG   = 256
	idleConnTimeout = 90 * time.Second
)

// Decision is RateGuard's answer for one request. Durations are whole
// seconds, rounded up.
type Decision struct {
	Allowed    bool `json:"allowed"`
	Limit      int  `json:"limit"`
	Remaining  int  `json:"remaining"`
	ResetAfter int  `json:"reset_after"`
	// RetryAfter is set when the request was refused.
	RetryAfter int `json:"retry_after"`
}

// StatusError is an answer from RateGuard other than a decision, such as
// 401 for an invalid API key or 403 when no policy applies to the caller.
type StatusError struct {
	StatusCode int
	Message    string
	RequestID  string
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("rateguard: %d %s (request %s)", e.StatusCode, e.Message, e.RequestID)
}

// Client asks RateGuard for decisions. It is safe for concurrent use; make
// one and share it.
type Client struct {
	checkURL string
	http     *http.Client
	timeout  time.Duration
	failOpen bool
	onError  func(r *http.Request, err error)
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout bounds each decision. The default is DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithFailOpen makes the middleware serve requests when RateGuard cannot
// be reached or answers with an error of its own. By default it refuses
// them with 503: an unreachable limiter must not mean an unlimited API.
// Refusals, invalid keys and callers without a policy are refused either
// way, because RateGuard did answer.
func WithFailOpen() Option {
	return func(c *Client) { c.failOpen = true }
}

// WithErrorHandler is told about every failed decision, for logging or
// metrics. It must not write to the response.
func WithErrorHandler(f func(r *http.Request, err error)) Option {
	return func(c *Client) { c.onError = f }
}

// WithHTTPClient replaces the client's own HTTP client, for tests or for a
// custom transport. The default keeps up to 256 idle connections.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// New returns a Client for the RateGuard at addr: an http:// or https://
// base URL, or unix:// and the absolute path of a sidecar's socket.
func New(addr string, opts ...Option) (*Client, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("rateguard: address %q: %w", addr, err)
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        idleConnsToRG,
		MaxIdleConnsPerHost: idleConnsToRG, // the default of 2 would reopen connections under load
		IdleConnTimeout:     idleConnTimeout,
	}
	c := &Client{http: &http.Client{Transport: transport}, timeout: DefaultTimeout, onError: func(*http.Request, error) {}}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("rateguard: address %q has no host", addr)
		}
		c.checkURL = strings.TrimSuffix(u.String(), "/") + "/v1/check"
	case "unix":
		if u.Path == "" || u.Host != "" {
			return nil, fmt.Errorf("rateguard: socket address %q must be unix:///absolute/path", addr)
		}
		socket := u.Path
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}
		// The host is never dialled; the socket is.
		c.checkURL = "http://rateguard/v1/check"
	default:
		return nil, fmt.Errorf("rateguard: address %q must start with http://, https:// or unix://", addr)
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

type checkRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Check asks RateGuard about r, a request this application received, and
// takes a token from its caller's bucket if it is allowed. Both outcomes
// are a Decision with a nil error; any other answer is a *StatusError.
//
// The caller's Authorization header is forwarded, and its address is
// appended to X-Forwarded-For. For RateGuard to believe the address, this
// application's network must be in RG_TRUSTED_PROXIES, or it must reach
// RateGuard over the sidecar socket.
func (c *Client) Check(ctx context.Context, r *http.Request) (Decision, error) {
	body, err := json.Marshal(checkRequest{Method: r.Method, Path: forwardedPath(r.URL.Path)})
	if err != nil {
		return Decision{}, fmt.Errorf("rateguard: encoding the request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.checkURL, bytes.NewReader(body))
	if err != nil {
		return Decision{}, fmt.Errorf("rateguard: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if xff := forwardedFor(r); xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("rateguard: %w", err)
	}
	defer func() {
		// Reading to the end lets the connection go back to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReplyBytes))
		_ = resp.Body.Close()
	}()
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return Decision{}, fmt.Errorf("rateguard: reading the answer: %w", err)
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusTooManyRequests {
		var d Decision
		if err := json.Unmarshal(reply, &d); err != nil {
			return Decision{}, fmt.Errorf("rateguard: decoding the decision: %w", err)
		}
		if d.Allowed != (resp.StatusCode == http.StatusOK) {
			return Decision{}, errors.New("rateguard: the decision contradicts its status")
		}
		return d, nil
	}
	se := &StatusError{StatusCode: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	var e struct {
		Error     string `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(reply, &e) == nil && e.Error != "" {
		se.Message, se.RequestID = e.Error, e.RequestID
	}
	return Decision{}, se
}

// forwardedPath is the path RateGuard is asked about. It is the decoded
// path, the one this application's router routes on, so an encoded
// character cannot route to /api/login while escaping its rule. RateGuard
// refuses ?, # and control characters in a path, so the few decoded paths
// that contain them are sent with those characters encoded again.
func forwardedPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	var b strings.Builder
	for i := range len(p) {
		switch ch := p[i]; {
		case ch == '?' || ch == '#' || ch < 0x20 || ch == 0x7f:
			fmt.Fprintf(&b, "%%%02X", ch)
		default:
			b.WriteByte(ch)
		}
	}
	out := b.String()
	if len(out) > maxPathLen {
		out = out[:maxPathLen]
	}
	return out
}

// forwardedFor is r's X-Forwarded-For with r's peer appended, as any proxy
// would add it.
func forwardedFor(r *http.Request) string {
	prior := strings.Join(r.Header.Values("X-Forwarded-For"), ", ")
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return prior
	}
	if prior == "" {
		return host
	}
	return prior + ", " + host
}
