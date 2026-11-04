package httptransport_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

const (
	policyFree    = "01a1a2fb-68ca-772e-a1e6-0729728300f1"
	policyMissing = "01a1a2fb-68ca-772e-a1e6-0729728300ff"
)

// stubPolicies stands in for the policy service: these tests cover HTTP
// status codes, validation and wire shapes. The real service on a real
// database is covered in test/integration.
type stubPolicies struct {
	created  []domain.NewPolicy
	updated  []domain.PolicyUpdate
	disabled []string
	lastPage domain.Page
	policies []domain.Policy
	err      error
}

func (s *stubPolicies) Create(_ context.Context, np domain.NewPolicy) (domain.Policy, error) {
	s.created = append(s.created, np)
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{
		ID: policyFree, Name: np.Name, Capacity: np.Capacity, WindowSeconds: np.WindowSeconds,
		RefillRate: 100.0 / 60, Enabled: np.Enabled, Default: np.Default, CreatedAt: created, UpdatedAt: created,
	}, nil
}

func (s *stubPolicies) Get(_ context.Context, id string) (domain.Policy, error) {
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	for _, p := range s.policies {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Policy{}, domain.ErrPolicyNotFound
}

func (s *stubPolicies) List(_ context.Context, page domain.Page) ([]domain.Policy, error) {
	s.lastPage = page
	return s.policies, s.err
}

func (s *stubPolicies) Update(_ context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error) {
	s.updated = append(s.updated, u)
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{ID: id, Name: "Free", Capacity: 100, WindowSeconds: 60, RefillRate: 100.0 / 60, Enabled: true}, nil
}

func (s *stubPolicies) Disable(_ context.Context, id string) (domain.Policy, error) {
	s.disabled = append(s.disabled, id)
	if s.err != nil {
		return domain.Policy{}, s.err
	}
	return domain.Policy{ID: id}, nil
}

func newPolicyServer(t *testing.T, d httptransport.Deps) *httptest.Server {
	t.Helper()
	d.Logger = slog.New(slog.DiscardHandler)
	d.ReadinessTimeout = time.Second
	d.AdminToken = adminToken
	srv := httptest.NewServer(httptransport.NewRouter(d))
	t.Cleanup(srv.Close)
	return srv
}

func TestPolicyRoutesRequireTheAdminToken(t *testing.T) {
	ps := &stubPolicies{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: ps})
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/policies", `{"name":"Free","capacity":100,"window_seconds":60}`},
		{http.MethodGet, "/v1/policies", ""},
		{http.MethodGet, "/v1/policies/" + policyFree, ""},
		{http.MethodPatch, "/v1/policies/" + policyFree, `{"capacity":5}`},
		{http.MethodDelete, "/v1/policies/" + policyFree, ""},
	} {
		if resp, _ := call(t, srv, rt.method, rt.path, rt.body, false); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", rt.method, rt.path, resp.StatusCode)
		}
	}
	if len(ps.created)+len(ps.updated)+len(ps.disabled) != 0 {
		t.Error("an unauthenticated request reached the service")
	}
}

func TestCreatePolicy(t *testing.T) {
	ps := &stubPolicies{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: ps})

	resp, body := call(t, srv, http.MethodPost, "/v1/policies", `{"name":"  Free  ","capacity":100,"window_seconds":60}`, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d %s, want 201", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/v1/policies/"+policyFree {
		t.Errorf("Location = %q", loc)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "name", "capacity", "window_seconds", "refill_rate", "enabled", "default", "created_at", "updated_at"} {
		if _, ok := got[field]; !ok {
			t.Errorf("response lacks %q: %s", field, body)
		}
	}
	want := domain.NewPolicy{Name: "Free", Capacity: 100, WindowSeconds: 60, Enabled: true}
	if len(ps.created) != 1 || ps.created[0] != want {
		t.Errorf("service got %+v, want %+v (trimmed name, enabled by default, refill rate left to the service)", ps.created, want)
	}

	_, body = call(t, srv, http.MethodPost, "/v1/policies", `{"name":"Burst","capacity":10,"window_seconds":1,"refill_rate":0.5,"enabled":false,"default":true}`, true)
	want = domain.NewPolicy{Name: "Burst", Capacity: 10, WindowSeconds: 1, RefillRate: 0.5, Enabled: false, Default: true}
	if len(ps.created) != 2 || ps.created[1] != want {
		t.Errorf("service got %+v, want %+v; body %s", ps.created[1:], want, body)
	}
}

func TestCreatePolicyValidation(t *testing.T) {
	tests := []struct{ body, want string }{
		{`{"capacity":100,"window_seconds":60}`, "name is required"},
		{`{"name":"   ","capacity":100,"window_seconds":60}`, "name is required"},
		{`{"name":"` + strings.Repeat("x", 101) + `","capacity":100,"window_seconds":60}`, "name must be at most 100 characters"},
		{`{"name":"x","window_seconds":60}`, "capacity must be at least 1"},
		{`{"name":"x","capacity":0,"window_seconds":60}`, "capacity must be at least 1"},
		{`{"name":"x","capacity":1000000001,"window_seconds":60}`, "capacity must be at most 1000000000"},
		{`{"name":"x","capacity":100}`, "window_seconds must be at least 1"},
		{`{"name":"x","capacity":100,"window_seconds":86401}`, "window_seconds must be at most 86400"},
		{`{"name":"x","capacity":100,"window_seconds":60,"refill_rate":0}`, "refill_rate must be greater than 0"},
		{`{"name":"x","capacity":100,"window_seconds":60,"refill_rate":-1}`, "refill_rate must be greater than 0"},
		{`{"name":"x","capacity":1.5,"window_seconds":60}`, `field "capacity" has the wrong type`},
		{`{"name":"x","capacity":99999999999999999999,"window_seconds":60}`, `field "capacity" has the wrong type`},
		{`{"name":"x","capacity":100,"window_seconds":60,"limit":5}`, `unknown field "limit"`},
	}
	for _, tt := range tests {
		ps := &stubPolicies{}
		srv := newPolicyServer(t, httptransport.Deps{Policies: ps})
		resp, body := call(t, srv, http.MethodPost, "/v1/policies", tt.body, true)
		if resp.StatusCode != http.StatusBadRequest || errorMessage(t, body) != tt.want {
			t.Errorf("%s: got %d %s, want 400 containing %q", tt.body, resp.StatusCode, body, tt.want)
		}
		if len(ps.created) != 0 {
			t.Errorf("%s: an invalid policy reached the service", tt.body)
		}
	}
}

func TestPolicyServiceErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
		msg  string
	}{
		{domain.ErrPolicyNameTaken, http.StatusConflict, "another policy already has this name"},
		{domain.ErrPolicyNotFound, http.StatusNotFound, "policy not found"},
		{errors.New("pool closed"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tt := range tests {
		srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{err: tt.err}})
		for _, rt := range []struct{ method, path, body string }{
			{http.MethodPost, "/v1/policies", `{"name":"Free","capacity":100,"window_seconds":60}`},
			{http.MethodPatch, "/v1/policies/" + policyFree, `{"name":"Free"}`},
		} {
			resp, body := call(t, srv, rt.method, rt.path, rt.body, true)
			if resp.StatusCode != tt.want || !strings.Contains(string(body), tt.msg) {
				t.Errorf("%v on %s: got %d %s, want %d %q", tt.err, rt.method, resp.StatusCode, body, tt.want, tt.msg)
			}
		}
	}
}

func TestListAndGetPolicies(t *testing.T) {
	ps := &stubPolicies{policies: []domain.Policy{
		{ID: policyFree, Name: "Free", Capacity: 100, WindowSeconds: 60, RefillRate: 100.0 / 60, Enabled: true, Default: true, CreatedAt: created, UpdatedAt: created},
	}}
	srv := newPolicyServer(t, httptransport.Deps{Policies: ps})

	resp, body := call(t, srv, http.MethodGet, "/v1/policies?limit=1&after="+policyMissing, "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	var list struct {
		Policies []struct {
			ID      string `json:"id"`
			Default bool   `json:"default"`
		} `json:"policies"`
		NextAfter string `json:"next_after"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Policies) != 1 || !list.Policies[0].Default || list.NextAfter != policyFree {
		t.Errorf("list = %s", body)
	}
	if ps.lastPage != (domain.Page{After: policyMissing, Limit: 1}) {
		t.Errorf("page = %+v", ps.lastPage)
	}
	if resp, _ := call(t, srv, http.MethodGet, "/v1/policies?limit=0", "", true); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", resp.StatusCode)
	}

	if resp, body := call(t, srv, http.MethodGet, "/v1/policies/"+policyFree, "", true); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"name":"Free"`) {
		t.Errorf("get = %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, srv, http.MethodGet, "/v1/policies/"+policyMissing, "", true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", resp.StatusCode)
	}
}

func TestUpdatePolicy(t *testing.T) {
	ps := &stubPolicies{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: ps})

	resp, body := call(t, srv, http.MethodPatch, "/v1/policies/"+policyFree, `{"name":" Free tier ","capacity":200,"default":true}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH = %d %s", resp.StatusCode, body)
	}
	u := ps.updated[0]
	if u.Name == nil || *u.Name != "Free tier" || u.Capacity == nil || *u.Capacity != 200 || u.Default == nil || !*u.Default ||
		u.WindowSeconds != nil || u.RefillRate != nil || u.Enabled != nil {
		t.Errorf("update = %+v, want only the given fields, name trimmed", u)
	}

	for _, tt := range []struct{ body, want string }{
		{`{}`, "request body must set at least one field"},
		{`{"name":null}`, "request body must set at least one field"},
		{`{"name":"  "}`, "name must not be blank"},
		{`{"capacity":0}`, "capacity must be at least 1"},
		{`{"window_seconds":0}`, "window_seconds must be at least 1"},
		{`{"refill_rate":0}`, "refill_rate must be greater than 0"},
		{`{"enabled":"yes"}`, `field "enabled" has the wrong type`},
	} {
		resp, body := call(t, srv, http.MethodPatch, "/v1/policies/"+policyFree, tt.body, true)
		if resp.StatusCode != http.StatusBadRequest || errorMessage(t, body) != tt.want {
			t.Errorf("PATCH %s = %d %s, want 400 %q", tt.body, resp.StatusCode, body, tt.want)
		}
	}
	if len(ps.updated) != 1 {
		t.Errorf("%d updates reached the service, want only the valid one", len(ps.updated))
	}
}

func TestDisablePolicy(t *testing.T) {
	ps := &stubPolicies{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: ps})
	resp, body := call(t, srv, http.MethodDelete, "/v1/policies/"+policyFree, "", true)
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Errorf("DELETE = %d %q, want 204 and no body", resp.StatusCode, body)
	}
	if len(ps.disabled) != 1 || ps.disabled[0] != policyFree {
		t.Errorf("disabled = %v", ps.disabled)
	}
}

// errorMessage returns the error field of an error response body.
func errorMessage(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decoding error body %s: %v", body, err)
	}
	return e.Error
}
