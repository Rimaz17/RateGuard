package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ReadinessCheck probes one dependency for /readyz. Check must honour
// cancellation of its context.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// Values reported per dependency in the /readyz body.
const (
	checkOK          = "ok"
	checkUnavailable = "unavailable"
)

type readinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// healthz is the liveness probe. It reports that the process is up and
// serving; it deliberately checks no dependencies, which is /readyz's job.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the readiness probe: 200 when every dependency answers within
// timeout, 503 otherwise. The body names each dependency's state but never
// its error, which goes to the log instead.
func readyz(logger *slog.Logger, checks []ReadinessCheck, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		// One goroutine per check, all joined before the handler returns
		// and all bounded by ctx, so a slow dependency costs one timeout
		// rather than one per check.
		errs := make([]error, len(checks))
		var wg sync.WaitGroup
		for i, c := range checks {
			wg.Go(func() { errs[i] = c.Check(ctx) })
		}
		wg.Wait()

		resp := readinessResponse{Status: "ready", Checks: make(map[string]string, len(checks))}
		status := http.StatusOK
		for i, c := range checks {
			if errs[i] == nil {
				resp.Checks[c.Name] = checkOK
				continue
			}
			resp.Checks[c.Name] = checkUnavailable
			resp.Status = "not ready"
			status = http.StatusServiceUnavailable
			logger.WarnContext(r.Context(), "readiness check failed",
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("check", c.Name),
				slog.Any("error", errs[i]),
			)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, resp)
	}
}
