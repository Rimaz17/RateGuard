package httptransport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

const ruleLogin = "01a1a2fb-68ca-772e-a1e6-0729728300e1"

type stubRules struct {
	created []domain.NewEndpointRule
	deleted []string
	rules   []domain.EndpointRule
	err     error
}

func (s *stubRules) CreateRule(_ context.Context, policyID string, nr domain.NewEndpointRule) (domain.EndpointRule, error) {
	s.created = append(s.created, nr)
	if s.err != nil {
		return domain.EndpointRule{}, s.err
	}
	return domain.EndpointRule{
		ID: ruleLogin, PolicyID: policyID, Method: nr.Method, PathPattern: nr.PathPattern,
		Capacity: nr.Capacity, WindowSeconds: nr.WindowSeconds, CreatedAt: created,
	}, nil
}

func (s *stubRules) ListRules(context.Context, string) ([]domain.EndpointRule, error) {
	return s.rules, s.err
}

func (s *stubRules) DeleteRule(_ context.Context, _, ruleID string) error {
	s.deleted = append(s.deleted, ruleID)
	return s.err
}

func TestRuleRoutesRequireTheAdminToken(t *testing.T) {
	rs := &stubRules{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: rs})
	base := "/v1/policies/" + policyFree + "/rules"
	for _, rt := range []struct{ method, path, body string }{
		{http.MethodGet, base, ""},
		{http.MethodPost, base, `{"method":"POST","path_pattern":"/api/login","capacity":5,"window_seconds":60}`},
		{http.MethodDelete, base + "/" + ruleLogin, ""},
	} {
		if resp, _ := call(t, srv, rt.method, rt.path, rt.body, false); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", rt.method, rt.path, resp.StatusCode)
		}
	}
	if len(rs.created)+len(rs.deleted) != 0 {
		t.Error("an unauthenticated request reached the service")
	}
}

func TestCreateRule(t *testing.T) {
	rs := &stubRules{}
	srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: rs})

	resp, body := call(t, srv, http.MethodPost, "/v1/policies/"+policyFree+"/rules",
		`{"method":" post ","path_pattern":"/api/login","capacity":5,"window_seconds":60}`, true)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d %s, want 201", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "policy_id", "method", "path_pattern", "capacity", "window_seconds", "created_at"} {
		if _, ok := got[field]; !ok {
			t.Errorf("response lacks %q: %s", field, body)
		}
	}
	want := domain.NewEndpointRule{Method: "post", PathPattern: "/api/login", Capacity: 5, WindowSeconds: 60}
	if len(rs.created) != 1 || rs.created[0] != want {
		t.Errorf("service got %+v, want %+v (method trimmed; upper-casing is the service's job)", rs.created, want)
	}
}

func TestCreateRuleValidation(t *testing.T) {
	tests := []struct{ body, want string }{
		{`{"path_pattern":"/a","capacity":5,"window_seconds":60}`, "method is required"},
		{`{"method":"   ","path_pattern":"/a","capacity":5,"window_seconds":60}`, "method is required"},
		{`{"method":"` + strings.Repeat("A", 17) + `","path_pattern":"/a","capacity":5,"window_seconds":60}`, "method must be at most 16 characters"},
		{`{"method":"GET","capacity":5,"window_seconds":60}`, "path_pattern is required"},
		{`{"method":"GET","path_pattern":"/` + strings.Repeat("a", 512) + `","capacity":5,"window_seconds":60}`, "path_pattern must be at most 512 characters"},
		{`{"method":"GET","path_pattern":"/a","window_seconds":60}`, "capacity must be at least 1"},
		{`{"method":"GET","path_pattern":"/a","capacity":5,"window_seconds":0}`, "window_seconds must be at least 1"},
		{`{"method":"GET","path_pattern":"/a","capacity":5,"window_seconds":60,"refill_rate":1}`, `unknown field "refill_rate"`},
	}
	for _, tt := range tests {
		rs := &stubRules{}
		srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: rs})
		resp, body := call(t, srv, http.MethodPost, "/v1/policies/"+policyFree+"/rules", tt.body, true)
		if resp.StatusCode != http.StatusBadRequest || errorMessage(t, body) != tt.want {
			t.Errorf("%.60s: got %d %s, want 400 %q", tt.body, resp.StatusCode, body, tt.want)
		}
		if len(rs.created) != 0 {
			t.Errorf("%.60s: an invalid rule reached the service", tt.body)
		}
	}
}

func TestRuleServiceErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
		msg  string
	}{
		{&domain.ValidationError{Field: "path_pattern", Reason: "must start with /"}, http.StatusBadRequest, "path_pattern must start with /"},
		{domain.ErrPolicyNotFound, http.StatusNotFound, "policy not found"},
		{domain.ErrEndpointRuleExists, http.StatusConflict, "the policy already has a rule for this method and path pattern"},
		{domain.ErrEndpointRuleLimit, http.StatusConflict, "policy already has 100 endpoint rules; delete one before adding another"},
	}
	for _, tt := range tests {
		srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: &stubRules{err: tt.err}})
		resp, body := call(t, srv, http.MethodPost, "/v1/policies/"+policyFree+"/rules",
			`{"method":"GET","path_pattern":"/a","capacity":5,"window_seconds":60}`, true)
		if resp.StatusCode != tt.want || errorMessage(t, body) != tt.msg {
			t.Errorf("%v: got %d %s, want %d %q", tt.err, resp.StatusCode, body, tt.want, tt.msg)
		}
	}

	srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: &stubRules{err: domain.ErrEndpointRuleNotFound}})
	resp, body := call(t, srv, http.MethodDelete, "/v1/policies/"+policyFree+"/rules/"+ruleLogin, "", true)
	if resp.StatusCode != http.StatusNotFound || errorMessage(t, body) != "endpoint rule not found" {
		t.Errorf("DELETE of a missing rule = %d %s, want 404", resp.StatusCode, body)
	}
}

func TestListAndDeleteRules(t *testing.T) {
	rs := &stubRules{rules: []domain.EndpointRule{
		{ID: ruleLogin, PolicyID: policyFree, Method: http.MethodPost, PathPattern: "/api/login", Capacity: 5, WindowSeconds: 60, CreatedAt: created},
	}}
	srv := newPolicyServer(t, httptransport.Deps{Policies: &stubPolicies{}, Rules: rs})

	resp, body := call(t, srv, http.MethodGet, "/v1/policies/"+policyFree+"/rules", "", true)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"path_pattern":"/api/login"`) {
		t.Errorf("list = %d %s", resp.StatusCode, body)
	}

	rs.rules = nil
	if _, body := call(t, srv, http.MethodGet, "/v1/policies/"+policyFree+"/rules", "", true); !strings.Contains(string(body), `"rules":[]`) {
		t.Errorf("an empty list = %s, want rules to be [] rather than null", body)
	}

	resp, body = call(t, srv, http.MethodDelete, "/v1/policies/"+policyFree+"/rules/"+ruleLogin, "", true)
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 || len(rs.deleted) != 1 || rs.deleted[0] != ruleLogin {
		t.Errorf("DELETE = %d %q, deleted %v", resp.StatusCode, body, rs.deleted)
	}
}

func TestRuleRoutesNeedPolicies(t *testing.T) {
	srv := newPolicyServer(t, httptransport.Deps{Rules: &stubRules{}})
	if resp, _ := call(t, srv, http.MethodGet, "/v1/policies/"+policyFree+"/rules", "", true); resp.StatusCode != http.StatusNotFound {
		t.Errorf("rules without the policy routes = %d, want 404 (not mounted)", resp.StatusCode)
	}
}
