package httptransport

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// Limits on the decision request (ADR 0015). Its body is a method and a
// path, so it needs far less room than an admin request.
const (
	maxCheckBodyBytes = 4 << 10
	maxMethodLen      = 32
	maxPathLen        = 2048
)

// Decider makes rate-limit decisions. *limiter.Service satisfies it.
type Decider interface {
	Check(ctx context.Context, id domain.Identity, method, requestPath string) (limiter.Decision, error)
}

// checkRequest is the body of POST /v1/check: the request the application
// received and wants a decision on.
type checkRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// decisionDTO is the body of a decision, allowed or not (ADR 0015).
// Durations are whole seconds, rounded up.
type decisionDTO struct {
	Allowed    bool   `json:"allowed"`
	Limit      int    `json:"limit"`
	Remaining  int    `json:"remaining"`
	ResetAfter int64  `json:"reset_after"`
	RetryAfter *int64 `json:"retry_after,omitempty"`
}

type decisionHandlers struct {
	logger  *slog.Logger
	decider Decider
}

// check handles POST /v1/check. It runs behind Identify, so the caller is
// already known. Nothing here logs unless the decision fails (ADR 0004).
func (h decisionHandlers) check(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if e := decodeJSONLimit(w, r, &req, maxCheckBodyBytes); e != nil {
		writeError(w, r, e.status, e.message)
		return
	}
	if e := req.validate(); e != nil {
		writeError(w, r, e.status, e.message)
		return
	}
	id, ok := IdentityFromContext(r.Context())
	if !ok {
		writeServiceError(w, r, h.logger, errors.New("decision request reached the handler without an identity"))
		return
	}

	d, err := h.decider.Check(r.Context(), id, req.Method, req.Path)
	switch {
	case err == nil:
		writeDecision(w, d)
	case errors.Is(err, domain.ErrNoPolicy):
		writeError(w, r, http.StatusForbidden, "no rate-limit policy applies to this caller")
	case r.Context().Err() != nil:
		// The caller hung up; nobody is waiting for an answer, and a
		// disconnect is not something an operator needs to read about.
	default:
		writeServiceError(w, r, h.logger, err)
	}
}

// validate checks the request against ADR 0015.
func (c checkRequest) validate() *requestError {
	switch {
	case c.Method == "":
		return badRequest("method is required")
	case len(c.Method) > maxMethodLen || !isToken(c.Method):
		return badRequest("method must be an HTTP method such as GET, of at most %d characters", maxMethodLen)
	case c.Path == "":
		return badRequest("path is required")
	case c.Path[0] != '/':
		return badRequest("path must start with /")
	case len(c.Path) > maxPathLen:
		return badRequest("path must be at most %d bytes", maxPathLen)
	case strings.ContainsAny(c.Path, "?#"):
		// /api/login?x would not match a rule on /api/login.
		return badRequest("path must not contain a query string or fragment; send the path only")
	case strings.ContainsFunc(c.Path, isControl):
		return badRequest("path must not contain control characters")
	}
	return nil
}

// isToken reports whether s is an HTTP token (RFC 9110 section 5.6.2), the
// syntax of a method.
func isToken(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// The rate-limit headers (ADR 0016). Each mirrors a body field.
const (
	headerLimit      = "X-RateLimit-Limit"
	headerRemaining  = "X-RateLimit-Remaining"
	headerReset      = "X-RateLimit-Reset"
	headerRetryAfter = "Retry-After"
)

// writeDecision sends a decision: 200 if allowed, 429 if not, with the same
// body either way (ADR 0015) and the rate-limit headers (ADR 0016).
func writeDecision(w http.ResponseWriter, d limiter.Decision) {
	body := toDecisionDTO(d)
	status := http.StatusOK
	if !d.Allowed {
		w.Header().Set(headerRetryAfter, strconv.FormatInt(*body.RetryAfter, 10))
		status = http.StatusTooManyRequests
	}
	writeDecisionBody(w, status, body)
}

// toDecisionDTO rounds a decision's durations up to whole seconds. A
// refusal always says at least one second.
func toDecisionDTO(d limiter.Decision) decisionDTO {
	body := decisionDTO{
		Allowed:    d.Allowed,
		Limit:      d.Limit,
		Remaining:  d.Remaining,
		ResetAfter: ceilSeconds(d.ResetAfter),
	}
	if !d.Allowed {
		retry := max(ceilSeconds(d.RetryAfter), 1)
		body.RetryAfter = &retry
	}
	return body
}

// writeDecisionBody sends the body with the X-RateLimit-* headers that
// mirror it.
func writeDecisionBody(w http.ResponseWriter, status int, body decisionDTO) {
	h := w.Header()
	h.Set(headerLimit, strconv.Itoa(body.Limit))
	h.Set(headerRemaining, strconv.Itoa(body.Remaining))
	h.Set(headerReset, strconv.FormatInt(body.ResetAfter, 10))
	h.Set("Cache-Control", "no-store")
	writeJSON(w, status, body)
}

// ceilSeconds rounds a duration up to whole seconds, so that waiting that
// long is always enough.
func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	secs := int64(d / time.Second)
	if d%time.Second != 0 && secs < math.MaxInt64 {
		secs++
	}
	return secs
}
