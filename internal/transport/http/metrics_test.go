package httptransport_test

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// timer records which decision operations were timed.
type timer struct {
	mu  sync.Mutex
	ops []string
}

func (t *timer) ObserveDecision(operation string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// A fast handler can take 0ns on a coarse clock (Windows), never less.
	if d < 0 {
		operation += " with a negative duration"
	}
	t.ops = append(t.ops, operation)
}

func TestDecisionsAreTimedWhateverTheirStatus(t *testing.T) {
	tm := &timer{}
	decider := &stubDecider{decision: limiter.Decision{Allowed: true, Limit: 10, Remaining: 9}}
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:        slog.New(slog.DiscardHandler),
		Decider:       decider,
		Authenticator: &stubAuth{valid: validKey},
		DecisionTimer: tm,
	}))
	t.Cleanup(srv.Close)
	c := &checkServer{srv: srv, decider: decider}

	c.post(t, validKey, `{"method":"GET","path":"/"}`)
	c.postTo(t, "/v1/peek", validKey, `{"method":"GET","path":"/"}`)
	c.post(t, "rg_bad_key", `{"method":"GET","path":"/"}`) // 401
	c.post(t, validKey, `{"method":"GET"}`)                // 400

	want := []string{"check", "peek", "check", "check"}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if strings.Join(tm.ops, ",") != strings.Join(want, ",") {
		t.Errorf("timed %v, want %v", tm.ops, want)
	}
}
