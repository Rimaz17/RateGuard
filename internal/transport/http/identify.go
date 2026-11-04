package httptransport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Authenticator resolves an API key to the identity it belongs to.
// *client.Authenticator satisfies it.
type Authenticator interface {
	Authenticate(ctx context.Context, key string) (domain.Identity, error)
}

const identityKey ctxKey = iota + 1

// IdentityFromContext returns the caller identity Identify stored, and
// whether there was one.
func IdentityFromContext(ctx context.Context) (domain.Identity, bool) {
	id, ok := ctx.Value(identityKey).(domain.Identity)
	return id, ok
}

// Identify works out who a request is from and stores it in the request
// context for the rate limiter. It guards the decision endpoints, not the
// admin API.
//
// A request with an Authorization header must carry a valid API key:
// a bad key is a 401, never a silent fallback to IP, or a caller could
// shed its key's limit by sending garbage. A request with no header is
// identified by its IP address (see ClientIP).
//
// Rejections log at DEBUG only, by key prefix: this sits on the hot path,
// where per-request logging is ruled out (ADR 0004).
func Identify(logger *slog.Logger, auth Authenticator, trusted TrustedProxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := identify(w, r, logger, auth, trusted)
			if !ok {
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
		})
	}
}

// identify resolves the caller or writes the rejection itself.
func identify(w http.ResponseWriter, r *http.Request, logger *slog.Logger, auth Authenticator, trusted TrustedProxies) (domain.Identity, bool) {
	ctx := r.Context()
	key, present := bearerToken(r)
	if !present {
		ip, ok := ClientIP(r, trusted)
		if !ok {
			unauthorized(w, r, "an API key is required on this connection")
			return domain.Identity{}, false
		}
		return domain.Identity{Kind: domain.IdentityIP, IP: ip}, true
	}
	if key == "" {
		unauthorized(w, r, `Authorization must be "Bearer <API key>"`)
		return domain.Identity{}, false
	}

	id, err := auth.Authenticate(ctx, key)
	switch {
	case err == nil:
		return id, true
	case errors.Is(err, domain.ErrInvalidAPIKey):
		logger.DebugContext(ctx, "rejected API key",
			slog.String("request_id", RequestIDFromContext(ctx)),
			slog.String("key_prefix", client.Redact(key)))
		unauthorized(w, r, "invalid API key")
	case errors.Is(err, domain.ErrClientDisabled):
		logger.DebugContext(ctx, "rejected key of disabled client",
			slog.String("request_id", RequestIDFromContext(ctx)),
			slog.String("key_prefix", client.Redact(key)))
		writeError(w, r, http.StatusForbidden, "client is disabled")
	default:
		writeServiceError(w, r, logger, err)
	}
	return domain.Identity{}, false
}

func unauthorized(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="rateguard"`)
	writeError(w, r, http.StatusUnauthorized, message)
}
