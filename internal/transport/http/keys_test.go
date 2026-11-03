package httptransport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

const (
	clientA = "01a1a2fb-68ca-772e-a1e6-00000000000a"
	keyOne  = "01a1a2fb-68ca-772e-a1e6-000000000001"
)

// stubKeys stands in for the client service's key operations.
type stubKeys struct {
	keys    []domain.APIKey
	revoked []string
	err     error
}

func (s *stubKeys) IssueKey(_ context.Context, clientID string) (domain.IssuedKey, error) {
	if s.err != nil {
		return domain.IssuedKey{}, s.err
	}
	return domain.IssuedKey{
		Key:       domain.APIKey{ID: keyOne, ClientID: clientID, Prefix: "rg_cus_cccccccccccccccc", CreatedAt: created},
		Plaintext: "rg_cus_cccccccccccccccc_" + strings.Repeat("d", 52),
	}, nil
}

func (s *stubKeys) ListKeys(context.Context, string) ([]domain.APIKey, error) {
	return s.keys, s.err
}

func (s *stubKeys) RevokeKey(_ context.Context, _, keyID string) (domain.APIKey, error) {
	if s.err != nil {
		return domain.APIKey{}, s.err
	}
	s.revoked = append(s.revoked, keyID)
	return domain.APIKey{ID: keyID, RevokedAt: created}, nil
}

func TestKeyRoutesRequireTheAdminToken(t *testing.T) {
	keys := &stubKeys{}
	srv := newAdminServerWithKeys(t, &stubRegistry{}, keys)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/clients/" + clientA + "/keys"},
		{http.MethodPost, "/v1/clients/" + clientA + "/keys"},
		{http.MethodDelete, "/v1/clients/" + clientA + "/keys/" + keyOne},
	} {
		if resp, _ := call(t, srv, rt.method, rt.path, "", false); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", rt.method, rt.path, resp.StatusCode)
		}
	}
	if len(keys.revoked) != 0 {
		t.Error("an unauthenticated request revoked a key")
	}
}

func TestIssueKey(t *testing.T) {
	srv := newAdminServerWithKeys(t, &stubRegistry{}, &stubKeys{})
	resp, body := call(t, srv, http.MethodPost, "/v1/clients/"+clientA+"/keys", "", true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d; body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var out struct {
		ClientID string `json:"client_id"`
		Prefix   string `json:"prefix"`
		Key      string `json:"key"`
		Active   bool   `json:"active"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if out.ClientID != clientA || !strings.HasPrefix(out.Key, out.Prefix+"_") || !out.Active {
		t.Errorf("body = %s, want the new active key for client A with its full value", body)
	}
}

func TestIssueKeyErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{domain.ErrActiveKeyLimit, http.StatusConflict},
		{domain.ErrClientNotFound, http.StatusNotFound},
	}
	for _, tt := range tests {
		srv := newAdminServerWithKeys(t, &stubRegistry{}, &stubKeys{err: tt.err})
		if resp, body := call(t, srv, http.MethodPost, "/v1/clients/"+clientA+"/keys", "", true); resp.StatusCode != tt.want {
			t.Errorf("%v: status = %d %s, want %d", tt.err, resp.StatusCode, body, tt.want)
		}
	}
}

func TestListKeysShowsPrefixesOnly(t *testing.T) {
	revokedAt := created.Add(time.Hour)
	keys := &stubKeys{keys: []domain.APIKey{
		{ID: keyOne, ClientID: clientA, Prefix: "rg_cus_aaaaaaaaaaaaaaaa", CreatedAt: created, RevokedAt: revokedAt},
		{ID: "01a1a2fb-68ca-772e-a1e6-000000000002", ClientID: clientA, Prefix: "rg_cus_bbbbbbbbbbbbbbbb", CreatedAt: created},
	}}
	srv := newAdminServerWithKeys(t, &stubRegistry{}, keys)

	resp, body := call(t, srv, http.MethodGet, "/v1/clients/"+clientA+"/keys", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		APIKeys []map[string]any `json:"api_keys"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.APIKeys) != 2 {
		t.Fatalf("api_keys = %v, want 2", out.APIKeys)
	}
	if out.APIKeys[0]["active"] != false || out.APIKeys[0]["revoked_at"] == nil {
		t.Errorf("first key = %v, want it revoked with a time", out.APIKeys[0])
	}
	if out.APIKeys[1]["active"] != true || out.APIKeys[1]["revoked_at"] != nil {
		t.Errorf("second key = %v, want it active with revoked_at null", out.APIKeys[1])
	}
	for _, k := range out.APIKeys {
		if _, ok := k["key"]; ok {
			t.Errorf("a listed key carries its full value: %v", k)
		}
	}
}

func TestRevokeKey(t *testing.T) {
	keys := &stubKeys{}
	srv := newAdminServerWithKeys(t, &stubRegistry{}, keys)

	resp, body := call(t, srv, http.MethodDelete, "/v1/clients/"+clientA+"/keys/"+keyOne, "", true)
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Errorf("DELETE = %d %q, want 204 with no body", resp.StatusCode, body)
	}
	if len(keys.revoked) != 1 || keys.revoked[0] != keyOne {
		t.Errorf("revoked %v, want [%s]", keys.revoked, keyOne)
	}

	srv = newAdminServerWithKeys(t, &stubRegistry{}, &stubKeys{err: domain.ErrAPIKeyNotFound})
	if resp, _ := call(t, srv, http.MethodDelete, "/v1/clients/"+clientA+"/keys/"+keyOne, "", true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE of a missing key = %d, want 404", resp.StatusCode)
	}
}
