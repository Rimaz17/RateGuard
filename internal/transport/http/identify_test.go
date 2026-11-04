package httptransport_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// stubAuth accepts one key. The real authenticator on a real database is
// covered in test/integration.
type stubAuth struct {
	valid, disabled string
	err             error
	calls           int
}

func (a *stubAuth) Authenticate(_ context.Context, key string) (domain.Identity, error) {
	a.calls++
	switch {
	case a.err != nil:
		return domain.Identity{}, a.err
	case key == a.valid:
		return domain.Identity{Kind: domain.IdentityAPIKey, ClientID: clientA, KeyPrefix: "rg_cus_aaaaaaaaaaaaaaaa"}, nil
	case key == a.disabled:
		return domain.Identity{}, domain.ErrClientDisabled
	default:
		return domain.Identity{}, domain.ErrInvalidAPIKey
	}
}

const validKey = "rg_cus_aaaaaaaaaaaaaaaa_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func serveIdentify(t *testing.T, auth httptransport.Authenticator, logs *bytes.Buffer, remote string, header ...string) (*httptest.ResponseRecorder, domain.Identity, bool) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var (
		got     domain.Identity
		reached bool
	)
	h := httptransport.RequestID(httptransport.Identify(logger, auth, proxies(t, "10.0.0.0/8"))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, reached = httptransport.IdentityFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/check", nil)
	req.RemoteAddr = remote
	for _, v := range header {
		req.Header.Add("Authorization", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got, reached
}

func TestIdentifyByAPIKey(t *testing.T) {
	var logs bytes.Buffer
	rec, id, reached := serveIdentify(t, &stubAuth{valid: validKey}, &logs, "203.0.113.7:1", "Bearer "+validKey)
	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("status = %d, reached = %v; want the request through", rec.Code, reached)
	}
	if id.Kind != domain.IdentityAPIKey || id.ClientID != clientA || id.IP.IsValid() {
		t.Errorf("identity = %+v, want the key's client and no IP", id)
	}
}

func TestIdentifyByIPWithoutAKey(t *testing.T) {
	var logs bytes.Buffer
	auth := &stubAuth{}
	rec, id, reached := serveIdentify(t, auth, &logs, "203.0.113.7:51000")
	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("status = %d; want the request through", rec.Code)
	}
	if id.Kind != domain.IdentityIP || id.IP != netip.MustParseAddr("203.0.113.7") || id.ClientID != "" {
		t.Errorf("identity = %+v, want IP 203.0.113.7", id)
	}
	if auth.calls != 0 {
		t.Error("the authenticator was consulted for a request without a key")
	}
}

func TestIdentifyRejections(t *testing.T) {
	tests := []struct {
		name    string
		remote  string
		header  []string
		auth    *stubAuth
		want    int
		wantMsg string
	}{
		{"invalid key never falls back to IP", "203.0.113.7:1", []string{"Bearer rg_cus_nope"}, &stubAuth{valid: validKey}, http.StatusUnauthorized, "invalid API key"},
		{"wrong scheme", "203.0.113.7:1", []string{"Basic " + validKey}, &stubAuth{valid: validKey}, http.StatusUnauthorized, "Bearer"},
		{"empty bearer", "203.0.113.7:1", []string{"Bearer "}, &stubAuth{valid: validKey}, http.StatusUnauthorized, "Bearer"},
		{"disabled client", "203.0.113.7:1", []string{"Bearer " + validKey}, &stubAuth{disabled: validKey}, http.StatusForbidden, "client is disabled"},
		{"storage failure", "203.0.113.7:1", []string{"Bearer " + validKey}, &stubAuth{err: errors.New("looking up API key: connection refused")}, http.StatusInternalServerError, "internal server error"},
		{"no key and no address", "@", nil, &stubAuth{}, http.StatusUnauthorized, "API key is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec, _, reached := serveIdentify(t, tt.auth, &logs, tt.remote, tt.header...)
			if reached {
				t.Fatal("a rejected request reached the handler")
			}
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.wantMsg) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tt.want, tt.wantMsg)
			}
			if tt.want == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("a 401 should carry a WWW-Authenticate challenge")
			}
		})
	}
}

func TestIdentifyNeverLogsTheKey(t *testing.T) {
	var logs bytes.Buffer
	presented := "rg_cus_aaaaaaaaaaaaaaaa_" + strings.Repeat("z", 52)
	serveIdentify(t, &stubAuth{valid: validKey}, &logs, "203.0.113.7:1", "Bearer "+presented)
	serveIdentify(t, &stubAuth{disabled: presented}, &logs, "203.0.113.7:1", "Bearer "+presented)

	if strings.Contains(logs.String(), strings.Repeat("z", 52)) {
		t.Errorf("logs contain the secret part of a key: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"key_prefix":"rg_cus_aaaaaaaaaaaaaaaa"`) {
		t.Errorf("rejections should log the key prefix at DEBUG, got: %s", logs.String())
	}
	if strings.Contains(logs.String(), `"level":"INFO"`) || strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("hot-path rejections must not log above DEBUG: %s", logs.String())
	}
}
