package httptransport

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// maxAuthorizationLen bounds the Authorization header before any work is
// done on it. Real values are under 100 bytes.
const maxAuthorizationLen = 512

// bearerToken extracts the credential from "Authorization: Bearer <token>".
// The scheme is case-insensitive (RFC 9110 section 11.1). present reports
// whether the header was sent at all, so callers can tell "no credential"
// from "a malformed one".
func bearerToken(r *http.Request) (token string, present bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", false
	}
	if len(values) > 1 || len(values[0]) > maxAuthorizationLen {
		return "", true
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(token), true
}

// AdminAuth admits only requests bearing the admin token (ADR 0008).
// Both tokens are hashed before the constant-time comparison so that
// neither their contents nor their lengths show in response timing.
func AdminAuth(token string) func(http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ := bearerToken(r)
			sum := sha256.Sum256([]byte(got))
			if got == "" || subtle.ConstantTimeCompare(sum[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="rateguard-admin"`)
				writeError(w, r, http.StatusUnauthorized, "a valid admin token is required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
