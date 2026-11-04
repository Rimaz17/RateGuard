package httptransport_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// stubRegistry stands in for the client service, not for Postgres: these
// tests are about HTTP status codes, validation and wire shapes. The real
// service on a real database is covered in test/integration.
type stubRegistry struct {
	registered []string
	lastPage   domain.Page
	clients    []domain.Client
	err        error
}

var created = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func (s *stubRegistry) Register(_ context.Context, name string) (domain.Client, domain.IssuedKey, error) {
	s.registered = append(s.registered, name)
	if s.err != nil {
		return domain.Client{}, domain.IssuedKey{}, s.err
	}
	c := domain.Client{ID: "01a1a2fb-68ca-772e-a1e6-072972830b19", Name: name, Status: domain.ClientActive, CreatedAt: created, UpdatedAt: created}
	k := domain.IssuedKey{
		Key:       domain.APIKey{ID: "01a1a2fb-68ca-772e-a1e6-072972830b20", ClientID: c.ID, Prefix: "rg_cus_aaaaaaaaaaaaaaaa", CreatedAt: created},
		Plaintext: "rg_cus_aaaaaaaaaaaaaaaa_" + strings.Repeat("b", 52),
	}
	return c, k, nil
}

func (s *stubRegistry) Get(_ context.Context, id string) (domain.Client, error) {
	if s.err != nil {
		return domain.Client{}, s.err
	}
	for _, c := range s.clients {
		if c.ID == id {
			return c, nil
		}
	}
	return domain.Client{}, domain.ErrClientNotFound
}

func (s *stubRegistry) List(_ context.Context, page domain.Page) ([]domain.Client, error) {
	s.lastPage = page
	if s.err != nil {
		return nil, s.err
	}
	return s.clients, nil
}

func newAdminServer(t *testing.T, reg httptransport.ClientRegistry) *httptest.Server {
	t.Helper()
	return newAdminServerWithKeys(t, reg, nil)
}

func newAdminServerWithKeys(t *testing.T, reg httptransport.ClientRegistry, keys httptransport.KeyManager) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:           slog.New(slog.DiscardHandler),
		ReadinessTimeout: time.Second,
		AdminToken:       adminToken,
		Clients:          reg,
		Keys:             keys,
	}))
	t.Cleanup(srv.Close)
	return srv
}

// response is what the tests need of an HTTP response, read and closed.
type response struct {
	StatusCode int
	Header     http.Header
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, authed bool) (response, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, r)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if authed {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return response{StatusCode: resp.StatusCode, Header: resp.Header}, b
}

func TestClientRoutesRequireTheAdminToken(t *testing.T) {
	reg := &stubRegistry{}
	srv := newAdminServer(t, reg)
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/clients", `{"name":"x"}`},
		{http.MethodGet, "/v1/clients", ""},
		{http.MethodGet, "/v1/clients/01a1a2fb-68ca-772e-a1e6-072972830b19", ""},
	} {
		resp, _ := call(t, srv, rt.method, rt.path, rt.body, false)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", rt.method, rt.path, resp.StatusCode)
		}
	}
	if len(reg.registered) != 0 {
		t.Error("an unauthenticated request reached the registry")
	}
}

func TestRegisterClient(t *testing.T) {
	reg := &stubRegistry{}
	srv := newAdminServer(t, reg)

	resp, body := call(t, srv, http.MethodPost, "/v1/clients", `{"name":"  Customer App  "}`, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a response carrying a key", got)
	}
	if got := resp.Header.Get("Location"); got != "/v1/clients/01a1a2fb-68ca-772e-a1e6-072972830b19" {
		t.Errorf("Location = %q", got)
	}
	if len(reg.registered) != 1 || reg.registered[0] != "Customer App" {
		t.Errorf("registered %q, want the trimmed name", reg.registered)
	}

	var out struct {
		Client struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			PolicyID *string `json:"policy_id"`
			Status   string  `json:"status"`
		} `json:"client"`
		APIKey struct {
			ID        string     `json:"id"`
			ClientID  string     `json:"client_id"`
			Prefix    string     `json:"prefix"`
			Key       string     `json:"key"`
			Active    bool       `json:"active"`
			RevokedAt *time.Time `json:"revoked_at"`
		} `json:"api_key"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if out.Client.Name != "Customer App" || out.Client.Status != "active" || out.Client.PolicyID != nil {
		t.Errorf("client = %+v", out.Client)
	}
	if out.APIKey.Key == "" || !strings.HasPrefix(out.APIKey.Key, out.APIKey.Prefix) || !out.APIKey.Active || out.APIKey.RevokedAt != nil {
		t.Errorf("api_key = %+v, want an active key with its full value", out.APIKey)
	}
}

func TestRegisterClientRejectsBadInput(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"missing name", `{}`, "name is required"},
		{"blank name", `{"name":"   "}`, "name is required"},
		{"name too long", `{"name":"` + strings.Repeat("n", 101) + `"}`, "name must be at most 100 characters"},
		{"unknown field", `{"name":"x","policy":"free"}`, `unknown field "policy"`},
		{"not json", `name=x`, "not valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &stubRegistry{}
			srv := newAdminServer(t, reg)
			resp, body := call(t, srv, http.MethodPost, "/v1/clients", tt.body, true)
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &e); err != nil {
				t.Fatalf("decoding %s: %v", body, err)
			}
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(e.Error, tt.want) {
				t.Errorf("got %d %q, want 400 containing %q", resp.StatusCode, e.Error, tt.want)
			}
			if len(reg.registered) != 0 {
				t.Error("invalid input reached the registry")
			}
		})
	}
}

func TestRegisterClientHidesInternalErrors(t *testing.T) {
	srv := newAdminServer(t, &stubRegistry{err: errors.New("inserting client: pq: relation \"clients\" does not exist")})
	resp, body := call(t, srv, http.MethodPost, "/v1/clients", `{"name":"x"}`, true)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if strings.Contains(string(body), "relation") || !strings.Contains(string(body), "request_id") {
		t.Errorf("body = %s, want a generic message with a request ID", body)
	}
}

func TestListClients(t *testing.T) {
	two := []domain.Client{
		{ID: "01a1a2fb-68ca-772e-a1e6-000000000001", Name: "A", Status: domain.ClientActive},
		{ID: "01a1a2fb-68ca-772e-a1e6-000000000002", Name: "B", Status: domain.ClientActive, PolicyID: "01a1a2fb-68ca-772e-a1e6-0000000000ff"},
	}
	reg := &stubRegistry{clients: two}
	srv := newAdminServer(t, reg)

	resp, body := call(t, srv, http.MethodGet, "/v1/clients", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body %s", resp.StatusCode, body)
	}
	if reg.lastPage != (domain.Page{Limit: 50}) {
		t.Errorf("page = %+v, want the default limit of 50 and no cursor", reg.lastPage)
	}
	var out struct {
		Clients []struct {
			ID       string  `json:"id"`
			PolicyID *string `json:"policy_id"`
		} `json:"clients"`
		NextAfter *string `json:"next_after"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Clients) != 2 || out.Clients[1].PolicyID == nil || out.NextAfter != nil {
		t.Errorf("body = %s, want two clients, B with a policy, and no next page", body)
	}

	_, body = call(t, srv, http.MethodGet, "/v1/clients?limit=2&after=01a1a2fb-68ca-772e-a1e6-000000000000", "", true)
	if reg.lastPage != (domain.Page{After: "01a1a2fb-68ca-772e-a1e6-000000000000", Limit: 2}) {
		t.Errorf("page = %+v, want the requested cursor and limit", reg.lastPage)
	}
	if !strings.Contains(string(body), `"next_after":"01a1a2fb-68ca-772e-a1e6-000000000002"`) {
		t.Errorf("a full page should carry next_after, got %s", body)
	}
}

func TestListClientsRejectsBadPaging(t *testing.T) {
	srv := newAdminServer(t, &stubRegistry{})
	for _, q := range []string{"limit=0", "limit=201", "limit=ten", "after=nope"} {
		resp, _ := call(t, srv, http.MethodGet, "/v1/clients?"+q, "", true)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", q, resp.StatusCode)
		}
	}
}

func TestGetClient(t *testing.T) {
	c := domain.Client{ID: "01a1a2fb-68ca-772e-a1e6-000000000001", Name: "A", Status: domain.ClientActive}
	srv := newAdminServer(t, &stubRegistry{clients: []domain.Client{c}})

	resp, body := call(t, srv, http.MethodGet, "/v1/clients/"+c.ID, "", true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"A"`) {
		t.Errorf("GET existing = %d %s", resp.StatusCode, body)
	}
	resp, body = call(t, srv, http.MethodGet, "/v1/clients/01a1a2fb-68ca-772e-a1e6-000000000009", "", true)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "client not found") {
		t.Errorf("GET missing = %d %s, want 404", resp.StatusCode, body)
	}
}
