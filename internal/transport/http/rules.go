package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// RuleManager creates, lists and deletes a policy's endpoint rules.
type RuleManager interface {
	CreateRule(ctx context.Context, policyID string, nr domain.NewEndpointRule) (domain.EndpointRule, error)
	ListRules(ctx context.Context, policyID string) ([]domain.EndpointRule, error)
	DeleteRule(ctx context.Context, policyID, ruleID string) error
}

type ruleHandlers struct {
	logger   *slog.Logger
	rules    RuleManager
	validate *validator.Validate
}

type endpointRuleDTO struct {
	ID            string    `json:"id"`
	PolicyID      string    `json:"policy_id"`
	Method        string    `json:"method"`
	PathPattern   string    `json:"path_pattern"`
	Capacity      int       `json:"capacity"`
	WindowSeconds int       `json:"window_seconds"`
	CreatedAt     time.Time `json:"created_at"`
}

func toEndpointRuleDTO(r domain.EndpointRule) endpointRuleDTO {
	return endpointRuleDTO{
		ID:            r.ID,
		PolicyID:      r.PolicyID,
		Method:        r.Method,
		PathPattern:   r.PathPattern,
		Capacity:      r.Capacity,
		WindowSeconds: r.WindowSeconds,
		CreatedAt:     r.CreatedAt,
	}
}

// The method and pattern are checked in full by the service (ADR 0010);
// these tags only bound their size and catch absence early.
type createRuleRequest struct {
	Method        string `json:"method" validate:"required,max=16"`
	PathPattern   string `json:"path_pattern" validate:"required,max=512"`
	Capacity      int    `json:"capacity" validate:"min=1,max=1000000000"`
	WindowSeconds int    `json:"window_seconds" validate:"min=1,max=86400"`
}

type ruleListResponse struct {
	Rules []endpointRuleDTO `json:"rules"`
}

// create handles POST /v1/policies/{policyID}/rules.
func (h ruleHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	req.Method = strings.TrimSpace(req.Method)
	if err := validateRequest(h.validate, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}

	rule, err := h.rules.CreateRule(r.Context(), chi.URLParam(r, "policyID"), domain.NewEndpointRule{
		Method:        req.Method,
		PathPattern:   req.PathPattern,
		Capacity:      req.Capacity,
		WindowSeconds: req.WindowSeconds,
	})
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, toEndpointRuleDTO(rule))
}

// list handles GET /v1/policies/{policyID}/rules.
func (h ruleHandlers) list(w http.ResponseWriter, r *http.Request) {
	rules, err := h.rules.ListRules(r.Context(), chi.URLParam(r, "policyID"))
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	resp := ruleListResponse{Rules: make([]endpointRuleDTO, len(rules))}
	for i, rule := range rules {
		resp.Rules[i] = toEndpointRuleDTO(rule)
	}
	writeJSON(w, http.StatusOK, resp)
}

// delete handles DELETE /v1/policies/{policyID}/rules/{ruleID}. Rules have
// no dependants, so unlike policies they are really deleted.
func (h ruleHandlers) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.rules.DeleteRule(r.Context(), chi.URLParam(r, "policyID"), chi.URLParam(r, "ruleID")); err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
