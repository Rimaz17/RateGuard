package httptransport

import (
	"encoding/json"
	"net/http"
)

// errorResponse is the body of every error RateGuard returns. Message is
// always written for the client: it never carries an internal error, a
// query or a stack. RequestID ties the response to the service log.
type errorResponse struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding failure (almost always
	// the client hanging up) has no better recovery than dropping the body.
	_ = json.NewEncoder(w).Encode(body)
}

// writeError sends a client-safe message with the request's ID.
func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message, RequestID: RequestIDFromContext(r.Context())})
}
