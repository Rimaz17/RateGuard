// Package httptransport exposes RateGuard over HTTP: the chi router, its
// handlers, middleware and wire types. The directory is transport/http, but
// the package is named httptransport so it never shadows net/http.
package httptransport

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Deps is everything the router needs. main builds it; nothing here
// constructs a dependency of its own.
type Deps struct {
	Logger *slog.Logger

	// Readiness lists the dependencies /readyz probes.
	Readiness []ReadinessCheck

	// ReadinessTimeout bounds the whole /readyz probe.
	ReadinessTimeout time.Duration
}

// NewRouter builds the HTTP handler for the whole service.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(d.Logger))

	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(d.Logger, d.Readiness, d.ReadinessTimeout))

	return r
}
