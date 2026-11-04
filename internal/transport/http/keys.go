package httptransport

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// KeyManager issues, lists and revokes a client's API keys.
type KeyManager interface {
	IssueKey(ctx context.Context, clientID string) (domain.IssuedKey, error)
	ListKeys(ctx context.Context, clientID string) ([]domain.APIKey, error)
	RevokeKey(ctx context.Context, clientID, keyID string) (domain.APIKey, error)
}

type keyHandlers struct {
	logger *slog.Logger
	keys   KeyManager
}

type keyListResponse struct {
	APIKeys []apiKeyDTO `json:"api_keys"`
}

// issue handles POST /v1/clients/{clientID}/keys: the first half of a
// rotation. The new key works at once, alongside the old one, until the
// old one is revoked.
func (h keyHandlers) issue(w http.ResponseWriter, r *http.Request) {
	key, err := h.keys.IssueKey(r.Context(), chi.URLParam(r, "clientID"))
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, toIssuedKeyDTO(key))
}

// list handles GET /v1/clients/{clientID}/keys. Keys are described by
// prefix only; there is no way to read a key back.
func (h keyHandlers) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.keys.ListKeys(r.Context(), chi.URLParam(r, "clientID"))
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	resp := keyListResponse{APIKeys: make([]apiKeyDTO, len(keys))}
	for i, k := range keys {
		resp.APIKeys[i] = toAPIKeyDTO(k)
	}
	writeJSON(w, http.StatusOK, resp)
}

// revoke handles DELETE /v1/clients/{clientID}/keys/{keyID}: the last step
// of a rotation, once callers have moved to the new key. It is idempotent.
func (h keyHandlers) revoke(w http.ResponseWriter, r *http.Request) {
	if _, err := h.keys.RevokeKey(r.Context(), chi.URLParam(r, "clientID"), chi.URLParam(r, "keyID")); err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
