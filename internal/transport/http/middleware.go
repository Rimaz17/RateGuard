package httptransport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RequestIDHeader carries the request ID in both directions: an inbound value
// from a trusted proxy is reused, and every response echoes the ID in use.
const RequestIDHeader = "X-Request-ID"

const maxRequestIDLen = 64

type ctxKey int

const requestIDKey ctxKey = iota

// RequestIDFromContext returns the request ID assigned by the router's
// middleware, or "" if the context did not come from a routed request.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestID assigns every request an ID, stores it in the request context
// and sets it on the response. It must run before anything that logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// validRequestID accepts inbound IDs only when they are short and made of
// characters that cannot break a log line or a response header.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	// crypto/rand.Read never returns an error on supported platforms; it
	// crashes the program irrecoverably instead (documented since Go 1.24).
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Recoverer turns a handler panic into a logged 500 so one bad request cannot
// take the process down. The client sees a generic message and the request
// ID; the panic value and stack go only to the log.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverPanic(logger, w, r)
			next.ServeHTTP(w, r)
		})
	}
}

// recoverPanic must be deferred directly: recover only stops a panic when
// called by the deferred function itself.
func recoverPanic(logger *slog.Logger, w http.ResponseWriter, r *http.Request) {
	rec := recover()
	if rec == nil {
		return
	}
	if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		// net/http uses this sentinel to abort a response on purpose.
		panic(rec)
	}
	ctx := r.Context()
	id := RequestIDFromContext(ctx)
	logger.ErrorContext(ctx, "handler panic",
		slog.String("request_id", id),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Any("panic", rec),
		slog.String("stack", string(debug.Stack())),
	)
	writeError(w, r, http.StatusInternalServerError, "internal server error")
}
