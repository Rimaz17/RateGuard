package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// PolicyAssigner puts a client on a policy, or takes it off any policy.
type PolicyAssigner interface {
	AssignPolicy(ctx context.Context, clientID, policyID string) (domain.Client, error)
}

type assignHandlers struct {
	logger   *slog.Logger
	assigner PolicyAssigner
}

// assignPolicyRequest keeps policy_id raw so that an absent field, which is
// a mistake, can be told apart from null, which removes the assignment.
type assignPolicyRequest struct {
	PolicyID json.RawMessage `json:"policy_id"`
}

// assign handles PUT /v1/clients/{clientID}/policy. The change reaches
// every instance's policy cache within milliseconds (ADR 0005).
func (h assignHandlers) assign(w http.ResponseWriter, r *http.Request) {
	var req assignPolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	policyID, perr := parsePolicyRef(req.PolicyID)
	if perr != nil {
		writeRequestError(w, r, perr)
		return
	}

	c, err := h.assigner.AssignPolicy(r.Context(), chi.URLParam(r, "clientID"), policyID)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(c))
}

// parsePolicyRef reads policy_id: a policy ID, or null for none.
func parsePolicyRef(raw json.RawMessage) (string, *requestError) {
	if len(raw) == 0 {
		return "", badRequest("policy_id is required; send null to remove the client's policy")
	}
	if bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || !domain.IsID(id) {
		return "", badRequest("policy_id must be a policy ID or null")
	}
	return id, nil
}
