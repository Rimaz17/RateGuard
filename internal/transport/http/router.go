// Package httptransport exposes RateGuard over HTTP: the chi router, its
// handlers, middleware and wire types. The directory is transport/http, but
// the package is named httptransport so it never shadows net/http.
package httptransport

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter builds the HTTP handler for the whole service.
func NewRouter(logger *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(logger))

	r.Get("/healthz", healthz)

	return r
}

// healthz is the liveness probe. It reports that the process is up and
// serving; it deliberately checks no dependencies, which is /readyz's job.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding failure (almost always
	// the client hanging up) has no better recovery than dropping the body.
	_ = json.NewEncoder(w).Encode(body)
}
