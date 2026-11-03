package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Page sizes for GET /v1/clients.
const (
	defaultPageLimit = 50
	maxPageLimit     = domain.MaxPageLimit
)

// ClientRegistry registers and looks up client applications.
type ClientRegistry interface {
	Register(ctx context.Context, name string) (domain.Client, domain.IssuedKey, error)
	Get(ctx context.Context, id string) (domain.Client, error)
	List(ctx context.Context, page domain.Page) ([]domain.Client, error)
}

type clientHandlers struct {
	logger   *slog.Logger
	registry ClientRegistry
	validate *validator.Validate
}

type registerClientRequest struct {
	Name string `json:"name" validate:"required,max=100"`
}

type registerClientResponse struct {
	Client clientDTO    `json:"client"`
	APIKey issuedKeyDTO `json:"api_key"`
}

type clientListResponse struct {
	Clients []clientDTO `json:"clients"`
	// NextAfter is the cursor for the next page, present when this page
	// is full.
	NextAfter string `json:"next_after,omitempty"`
}

// register handles POST /v1/clients: one call creates the client and
// returns its first key, which is never shown again.
func (h clientHandlers) register(w http.ResponseWriter, r *http.Request) {
	var req registerClientRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validateRequest(h.validate, &req); err != nil {
		writeRequestError(w, r, err)
		return
	}

	c, key, err := h.registry.Register(r.Context(), req.Name)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Location", "/v1/clients/"+c.ID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, registerClientResponse{Client: toClientDTO(c), APIKey: toIssuedKeyDTO(key)})
}

// list handles GET /v1/clients?limit=N&after=ID.
func (h clientHandlers) list(w http.ResponseWriter, r *http.Request) {
	page, perr := parsePage(r)
	if perr != nil {
		writeRequestError(w, r, perr)
		return
	}
	cs, err := h.registry.List(r.Context(), page)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	resp := clientListResponse{Clients: make([]clientDTO, len(cs))}
	for i, c := range cs {
		resp.Clients[i] = toClientDTO(c)
	}
	if len(cs) == page.Limit {
		resp.NextAfter = cs[len(cs)-1].ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// get handles GET /v1/clients/{clientID}.
func (h clientHandlers) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.registry.Get(r.Context(), chi.URLParam(r, "clientID"))
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(c))
}

func parsePage(r *http.Request) (domain.Page, *requestError) {
	q := r.URL.Query()
	page := domain.Page{After: q.Get("after"), Limit: defaultPageLimit}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageLimit {
			return domain.Page{}, badRequest("limit must be an integer from 1 to %d", maxPageLimit)
		}
		page.Limit = n
	}
	if page.After != "" && !domain.IsID(page.After) {
		return domain.Page{}, badRequest("after must be an ID from a previous page")
	}
	return page, nil
}

func writeRequestError(w http.ResponseWriter, r *http.Request, err *requestError) {
	writeError(w, r, err.status, err.message)
}
