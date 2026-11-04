package httptransport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

type assignment struct{ clientID, policyID string }

type stubAssigner struct {
	calls []assignment
	err   error
}

func (s *stubAssigner) AssignPolicy(_ context.Context, clientID, policyID string) (domain.Client, error) {
	s.calls = append(s.calls, assignment{clientID, policyID})
	if s.err != nil {
		return domain.Client{}, s.err
	}
	return domain.Client{ID: clientID, Name: "Customer App", PolicyID: policyID, Status: domain.ClientActive, CreatedAt: created, UpdatedAt: created}, nil
}

func TestAssignPolicyRequiresTheAdminToken(t *testing.T) {
	a := &stubAssigner{}
	srv := newPolicyServer(t, httptransport.Deps{Clients: &stubRegistry{}, Assigner: a})
	resp, _ := call(t, srv, http.MethodPut, "/v1/clients/"+clientA+"/policy", `{"policy_id":"`+policyFree+`"}`, false)
	if resp.StatusCode != http.StatusUnauthorized || len(a.calls) != 0 {
		t.Errorf("PUT without a token = %d with %d calls, want 401 and none", resp.StatusCode, len(a.calls))
	}
}

func TestAssignPolicy(t *testing.T) {
	a := &stubAssigner{}
	srv := newPolicyServer(t, httptransport.Deps{Clients: &stubRegistry{}, Assigner: a})
	path := "/v1/clients/" + clientA + "/policy"

	resp, body := call(t, srv, http.MethodPut, path, `{"policy_id":"`+policyFree+`"}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("assign = %d %s", resp.StatusCode, body)
	}
	var c struct {
		ID       string  `json:"id"`
		PolicyID *string `json:"policy_id"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if c.ID != clientA || c.PolicyID == nil || *c.PolicyID != policyFree {
		t.Errorf("response = %s, want the client on the policy", body)
	}

	resp, body = call(t, srv, http.MethodPut, path, `{"policy_id":null}`, true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unassign = %d %s", resp.StatusCode, body)
	}
	c.PolicyID = nil
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatal(err)
	}
	if c.PolicyID != nil {
		t.Errorf("response = %s, want policy_id null", body)
	}

	want := []assignment{{clientA, policyFree}, {clientA, ""}}
	if len(a.calls) != 2 || a.calls[0] != want[0] || a.calls[1] != want[1] {
		t.Errorf("calls = %v, want %v", a.calls, want)
	}
}

func TestAssignPolicyValidation(t *testing.T) {
	for _, tt := range []struct{ body, want string }{
		{`{}`, "policy_id is required; send null to remove the client's policy"},
		{`{"policy_id":""}`, "policy_id must be a policy ID or null"},
		{`{"policy_id":"free"}`, "policy_id must be a policy ID or null"},
		{`{"policy_id":42}`, "policy_id must be a policy ID or null"},
		{`{"policy_id":{"id":"x"}}`, "policy_id must be a policy ID or null"},
		{`{"policy":"` + policyFree + `"}`, `unknown field "policy"`},
	} {
		a := &stubAssigner{}
		srv := newPolicyServer(t, httptransport.Deps{Clients: &stubRegistry{}, Assigner: a})
		resp, body := call(t, srv, http.MethodPut, "/v1/clients/"+clientA+"/policy", tt.body, true)
		if resp.StatusCode != http.StatusBadRequest || errorMessage(t, body) != tt.want {
			t.Errorf("%s: got %d %s, want 400 %q", tt.body, resp.StatusCode, body, tt.want)
		}
		if len(a.calls) != 0 {
			t.Errorf("%s: an invalid request reached the service", tt.body)
		}
	}
}

func TestAssignPolicyErrors(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
		msg  string
	}{
		{domain.ErrClientNotFound, http.StatusNotFound, "client not found"},
		{domain.ErrPolicyNotFound, http.StatusNotFound, "policy not found"},
		{domain.ErrPolicyDisabled, http.StatusConflict, "policy is disabled; enable it before assigning it"},
	} {
		srv := newPolicyServer(t, httptransport.Deps{Clients: &stubRegistry{}, Assigner: &stubAssigner{err: tt.err}})
		resp, body := call(t, srv, http.MethodPut, "/v1/clients/"+clientA+"/policy", `{"policy_id":"`+policyFree+`"}`, true)
		if resp.StatusCode != tt.want || errorMessage(t, body) != tt.msg {
			t.Errorf("%v: got %d %s, want %d %q", tt.err, resp.StatusCode, body, tt.want, tt.msg)
		}
	}
}
