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

// Deps is everything the router needs. main builds it; the router builds
// nothing with I/O of its own.
type Deps struct {
	Logger *slog.Logger

	// Readiness lists the dependencies /readyz probes.
	Readiness []ReadinessCheck

	// ReadinessTimeout bounds the whole /readyz probe.
	ReadinessTimeout time.Duration

	// Metrics serves GET /metrics, the Prometheus scrape. When nil, the
	// route is not mounted.
	Metrics http.Handler

	// AdminToken guards every admin route (ADR 0008). Empty locks them
	// all: no presented token can match it.
	AdminToken string

	// Clients serves the /v1/clients admin routes. When nil, they are not
	// mounted, which suits tests that exercise only the probes.
	Clients ClientRegistry

	// Keys serves /v1/clients/{clientID}/keys. It is mounted only
	// alongside Clients.
	Keys KeyManager

	// Policies serves the /v1/policies admin routes. When nil, they are
	// not mounted.
	Policies PolicyManager

	// Rules serves /v1/policies/{policyID}/rules. It is mounted only
	// alongside Policies.
	Rules RuleManager

	// Assigner serves PUT /v1/clients/{clientID}/policy. It is mounted only
	// alongside Clients.
	Assigner PolicyAssigner

	// Decider serves POST /v1/check and /v1/peek, the hot path. They are
	// mounted only when Authenticator is set too, which identifies callers
	// by API key; callers without one are identified by IP, trusting
	// X-Forwarded-For only from TrustedProxies.
	Decider        Decider
	Authenticator  Authenticator
	TrustedProxies TrustedProxies
}

// NewRouter builds the HTTP handler for the whole service.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(RequestID)
	r.Use(Recoverer(d.Logger))

	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(d.Logger, d.Readiness, d.ReadinessTimeout))
	if d.Metrics != nil {
		// Unauthenticated like the probes: its labels never name a client
		// or a key (ADR 0014).
		r.Method(http.MethodGet, "/metrics", d.Metrics)
	}

	if d.Decider != nil && d.Authenticator != nil {
		dh := decisionHandlers{logger: d.Logger, decider: d.Decider}
		identify := Identify(d.Logger, d.Authenticator, d.TrustedProxies)
		r.With(identify).Post("/v1/check", dh.check)
		r.With(identify).Post("/v1/peek", dh.peek)
	}

	if d.Clients != nil {
		ch := clientHandlers{logger: d.Logger, registry: d.Clients, validate: newValidator()}
		r.Route("/v1/clients", func(r chi.Router) {
			r.Use(AdminAuth(d.AdminToken))
			r.Post("/", ch.register)
			r.Get("/", ch.list)
			r.Get("/{clientID}", ch.get)
			if d.Keys != nil {
				kh := keyHandlers{logger: d.Logger, keys: d.Keys}
				r.Get("/{clientID}/keys", kh.list)
				r.Post("/{clientID}/keys", kh.issue)
				r.Delete("/{clientID}/keys/{keyID}", kh.revoke)
			}
			if d.Assigner != nil {
				ah := assignHandlers{logger: d.Logger, assigner: d.Assigner}
				r.Put("/{clientID}/policy", ah.assign)
			}
		})
	}

	if d.Policies != nil {
		ph := policyHandlers{logger: d.Logger, policies: d.Policies, validate: newValidator()}
		r.Route("/v1/policies", func(r chi.Router) {
			r.Use(AdminAuth(d.AdminToken))
			r.Post("/", ph.create)
			r.Get("/", ph.list)
			r.Get("/{policyID}", ph.get)
			r.Patch("/{policyID}", ph.update)
			r.Delete("/{policyID}", ph.disable)
			if d.Rules != nil {
				rh := ruleHandlers{logger: d.Logger, rules: d.Rules, validate: ph.validate}
				r.Get("/{policyID}/rules", rh.list)
				r.Post("/{policyID}/rules", rh.create)
				r.Delete("/{policyID}/rules/{ruleID}", rh.delete)
			}
		})
	}

	return r
}
