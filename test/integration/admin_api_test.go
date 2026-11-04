//go:build integration

package integration

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

const testAdminToken = "integration-admin-token-0123456789"

// newAdminAPI serves the real router on the real service and database, as
// main wires it.
func newAdminAPI(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	svc := newClientService(pool)
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:           slog.New(slog.DiscardHandler),
		ReadinessTimeout: time.Second,
		AdminToken:       testAdminToken,
		Clients:          svc,
		Keys:             svc,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func adminCall(t *testing.T, srv *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, r)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, b
}

type registered struct {
	Client struct {
		ID string `json:"id"`
	} `json:"client"`
	APIKey struct {
		ID     string `json:"id"`
		Prefix string `json:"prefix"`
		Key    string `json:"key"`
	} `json:"api_key"`
}

func registerViaAPI(t *testing.T, srv *httptest.Server, name string) registered {
	t.Helper()
	code, body := adminCall(t, srv, http.MethodPost, "/v1/clients", `{"name":"`+name+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/clients = %d %s", code, body)
	}
	var out registered
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if !fullKeyShape.MatchString(out.APIKey.Key) {
		t.Fatalf("registration returned key %q, want a full key", out.APIKey.Key)
	}
	return out
}

// TestKeyIsReturnedOnceAndNeverAgain is half of phase 2's done-when: after
// the registration response, no endpoint and no column holds the key.
func TestKeyIsReturnedOnceAndNeverAgain(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	srv := newAdminAPI(t, pool)

	reg := registerViaAPI(t, srv, "Customer App")
	secret := strings.TrimPrefix(reg.APIKey.Key, reg.APIKey.Prefix+"_")

	code, rotated := adminCall(t, srv, http.MethodPost, "/v1/clients/"+reg.Client.ID+"/keys", "")
	if code != http.StatusCreated {
		t.Fatalf("issuing a second key = %d %s", code, rotated)
	}

	for _, path := range []string{
		"/v1/clients",
		"/v1/clients/" + reg.Client.ID,
		"/v1/clients/" + reg.Client.ID + "/keys",
	} {
		code, body := adminCall(t, srv, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, body)
		}
		if strings.Contains(string(body), secret) {
			t.Errorf("GET %s returns the key's secret: %s", path, body)
		}
		if !strings.Contains(string(body), reg.Client.ID) {
			t.Errorf("GET %s does not mention the client; is this the right response? %s", path, body)
		}
	}
	if strings.Contains(string(rotated), secret) {
		t.Error("issuing a second key returned the first key's secret")
	}

	// Storage: no column of any table holds the key or its secret, as
	// text or hex. Only the SHA-256 digest is stored.
	rows, err := pool.Query(ctx, `
		SELECT row_to_json(t)::text FROM (
			SELECT id::text, client_id::text, key_prefix, encode(key_hash, 'hex') AS key_hash, created_at, revoked_at FROM api_keys
		) t
		UNION ALL SELECT row_to_json(c)::text FROM clients c`)
	if err != nil {
		t.Fatalf("dumping tables: %v", err)
	}
	defer rows.Close()
	hexSecret := hex.EncodeToString([]byte(secret))
	n := 0
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		n++
		if strings.Contains(row, secret) || strings.Contains(row, hexSecret) {
			t.Errorf("a stored row holds the key: %s", row)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("dumping tables: %v", err)
	}
	if n != 3 {
		t.Errorf("dumped %d rows, want 3 (one client, two keys)", n)
	}
}

func TestAdminAPIRejectsMissingToken(t *testing.T) {
	srv := newAdminAPI(t, migratedPool(t))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/clients", strings.NewReader(`{"name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST without a token = %d, want 401", resp.StatusCode)
	}
}

func TestAdminAPIErrorsOnTheRealStack(t *testing.T) {
	srv := newAdminAPI(t, migratedPool(t))
	reg := registerViaAPI(t, srv, "Merchant App")
	base := "/v1/clients/" + reg.Client.ID

	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/clients/01a1a2fb-68ca-772e-a1e6-072972830b19", http.StatusNotFound},
		{http.MethodGet, "/v1/clients/not-a-uuid", http.StatusNotFound},
		{http.MethodGet, "/v1/clients/not-a-uuid/keys", http.StatusNotFound},
		{http.MethodPost, "/v1/clients/01a1a2fb-68ca-772e-a1e6-072972830b19/keys", http.StatusNotFound},
		{http.MethodDelete, base + "/keys/01a1a2fb-68ca-772e-a1e6-072972830b19", http.StatusNotFound},
		{http.MethodDelete, base + "/keys/garbage", http.StatusNotFound},
		{http.MethodPost, base + "/keys", http.StatusCreated},
		{http.MethodPost, base + "/keys", http.StatusConflict},
	}
	for _, tt := range tests {
		if code, body := adminCall(t, srv, tt.method, tt.path, ""); code != tt.want {
			t.Errorf("%s %s = %d %s, want %d", tt.method, tt.path, code, body, tt.want)
		}
	}
}
