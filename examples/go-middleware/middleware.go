package rateguard

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Middleware asks RateGuard about every request before next sees it:
//
//   - Allowed: next serves it, with the X-RateLimit-* headers set.
//   - Refused: 429 with the same headers and Retry-After.
//   - An invalid key (401) or no applicable policy (403): refused with
//     RateGuard's status and message, since RateGuard did answer.
//   - RateGuard unreachable, or any other error: 503, or served by next
//     with WithFailOpen. The error goes to the WithErrorHandler hook.
func (c *Client) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, err := c.Check(r.Context(), r)
		if err != nil {
			c.refuseOrServe(w, r, next, err)
			return
		}
		h := w.Header()
		h.Set("X-RateLimit-Limit", strconv.Itoa(d.Limit))
		h.Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
		h.Set("X-RateLimit-Reset", strconv.Itoa(d.ResetAfter))
		if !d.Allowed {
			h.Set("Retry-After", strconv.Itoa(d.RetryAfter))
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c *Client) refuseOrServe(w http.ResponseWriter, r *http.Request, next http.Handler, err error) {
	if r.Context().Err() != nil {
		return // the caller left; nobody is waiting for an answer
	}
	var se *StatusError
	if errors.As(err, &se) && (se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden) {
		writeError(w, se.StatusCode, se.Message)
		return
	}
	c.onError(r, err)
	if c.failOpen {
		next.ServeHTTP(w, r)
		return
	}
	writeError(w, http.StatusServiceUnavailable, "rate limiter unavailable")
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is sent; a failed write means the client left.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
