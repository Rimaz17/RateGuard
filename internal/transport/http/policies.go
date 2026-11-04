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

// PolicyManager creates, reads, updates and disables policies.
type PolicyManager interface {
	Create(ctx context.Context, np domain.NewPolicy) (domain.Policy, error)
	Get(ctx context.Context, id string) (domain.Policy, error)
	List(ctx context.Context, page domain.Page) ([]domain.Policy, error)
	Update(ctx context.Context, id string, u domain.PolicyUpdate) (domain.Policy, error)
	Disable(ctx context.Context, id string) (domain.Policy, error)
}

type policyHandlers struct {
	logger   *slog.Logger
	policies PolicyManager
	validate *validator.Validate
}

type policyDTO struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Capacity      int       `json:"capacity"`
	WindowSeconds int       `json:"window_seconds"`
	RefillRate    float64   `json:"refill_rate"`
	Enabled       bool      `json:"enabled"`
	Default       bool      `json:"default"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func toPolicyDTO(p domain.Policy) policyDTO {
	return policyDTO{
		ID:            p.ID,
		Name:          p.Name,
		Capacity:      p.Capacity,
		WindowSeconds: p.WindowSeconds,
		RefillRate:    p.RefillRate,
		Enabled:       p.Enabled,
		Default:       p.Default,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
	}
}

// Bounds on policy numbers: capacity fits the INTEGER column with room to
// spare, and a window longer than a day is not what a rate limit is for.
type createPolicyRequest struct {
	Name          string   `json:"name" validate:"required,max=100"`
	Capacity      int      `json:"capacity" validate:"min=1,max=1000000000"`
	WindowSeconds int      `json:"window_seconds" validate:"min=1,max=86400"`
	RefillRate    *float64 `json:"refill_rate" validate:"omitempty,gt=0,lte=1000000000"`
	Enabled       *bool    `json:"enabled"`
	Default       bool     `json:"default"`
}

type updatePolicyRequest struct {
	Name          *string  `json:"name" validate:"omitempty,max=100"`
	Capacity      *int     `json:"capacity" validate:"omitempty,min=1,max=1000000000"`
	WindowSeconds *int     `json:"window_seconds" validate:"omitempty,min=1,max=86400"`
	RefillRate    *float64 `json:"refill_rate" validate:"omitempty,gt=0,lte=1000000000"`
	Enabled       *bool    `json:"enabled"`
	Default       *bool    `json:"default"`
}

type policyListResponse struct {
	Policies []policyDTO `json:"policies"`
	// NextAfter is the cursor for the next page, present when this page
	// is full.
	NextAfter string `json:"next_after,omitempty"`
}

// create handles POST /v1/policies.
func (h policyHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createPolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validateRequest(h.validate, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}

	np := domain.NewPolicy{
		Name:          req.Name,
		Capacity:      req.Capacity,
		WindowSeconds: req.WindowSeconds,
		Enabled:       req.Enabled == nil || *req.Enabled,
		Default:       req.Default,
	}
	if req.RefillRate != nil {
		np.RefillRate = *req.RefillRate
	}
	p, err := h.policies.Create(r.Context(), np)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Location", "/v1/policies/"+p.ID)
	writeJSON(w, http.StatusCreated, toPolicyDTO(p))
}

// list handles GET /v1/policies?limit=N&after=ID.
func (h policyHandlers) list(w http.ResponseWriter, r *http.Request) {
	page, perr := parsePage(r)
	if perr != nil {
		writeRequestError(w, r, perr)
		return
	}
	ps, err := h.policies.List(r.Context(), page)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	resp := policyListResponse{Policies: make([]policyDTO, len(ps))}
	for i, p := range ps {
		resp.Policies[i] = toPolicyDTO(p)
	}
	if len(ps) == page.Limit {
		resp.NextAfter = ps[len(ps)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// get handles GET /v1/policies/{policyID}.
func (h policyHandlers) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.policies.Get(r.Context(), chi.URLParam(r, "policyID"))
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(p))
}

// update handles PATCH /v1/policies/{policyID}. Omitted fields are left as
// they are; changing capacity or window without refill_rate recomputes the
// refill rate (ADR 0009).
func (h policyHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req updatePolicyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			writeRequestError(w, r, badRequest("name must not be blank"))
			return
		}
		req.Name = &trimmed
	}
	if err := validateRequest(h.validate, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	u := domain.PolicyUpdate{
		Name:          req.Name,
		Capacity:      req.Capacity,
		WindowSeconds: req.WindowSeconds,
		RefillRate:    req.RefillRate,
		Enabled:       req.Enabled,
		Default:       req.Default,
	}
	if u == (domain.PolicyUpdate{}) {
		writeRequestError(w, r, badRequest("request body must set at least one field"))
		return
	}

	p, err := h.policies.Update(r.Context(), chi.URLParam(r, "policyID"), u)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(p))
}

// disable handles DELETE /v1/policies/{policyID}. Policies are disabled,
// never deleted, so no client is left pointing at a missing row; its
// clients fall back to the default policy (ADR 0009). It is idempotent.
func (h policyHandlers) disable(w http.ResponseWriter, r *http.Request) {
	if _, err := h.policies.Disable(r.Context(), chi.URLParam(r, "policyID")); err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
