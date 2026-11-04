package httptransport

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// writeServiceError answers with the response a service error deserves.
// Known domain errors get a fixed, client-safe message; their wrapped text
// is never sent, because wrapping adds internal context. Anything else is
// logged with the request ID and answered with a generic 500.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, domain.ErrClientNotFound):
		writeError(w, r, http.StatusNotFound, "client not found")
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		writeError(w, r, http.StatusNotFound, "API key not found")
	case errors.Is(err, domain.ErrActiveKeyLimit):
		writeError(w, r, http.StatusConflict,
			fmt.Sprintf("client already has %d active API keys; revoke one before issuing another", domain.MaxActiveKeys))
	case errors.Is(err, domain.ErrPolicyNotFound):
		writeError(w, r, http.StatusNotFound, "policy not found")
	case errors.Is(err, domain.ErrPolicyNameTaken):
		writeError(w, r, http.StatusConflict, "another policy already has this name")
	default:
		logger.ErrorContext(r.Context(), "request failed",
			slog.String("request_id", RequestIDFromContext(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Any("error", err),
		)
		writeError(w, r, http.StatusInternalServerError, "internal server error")
	}
}
