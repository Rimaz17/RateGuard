package httptransport

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Decision operations, as the latency histogram labels them.
const (
	opCheck = "check"
	opPeek  = "peek"
)

// DecisionTimer observes how long the server took to answer a decision
// request. *Metrics satisfies it.
type DecisionTimer interface {
	ObserveDecision(operation string, d time.Duration)
}

// Metrics exports the decision endpoints' latency to Prometheus (ADR 0019).
type Metrics struct {
	durations map[string]prometheus.Observer
}

var _ DecisionTimer = (*Metrics)(nil)

// NewMetrics creates the HTTP metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	durations := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "rateguard_decision_duration_seconds",
		Help: "Time from receiving a decision request to answering it, by operation (check or peek), every status included.",
		// Fine below a millisecond, where a decision normally lands, and
		// up to a second for a struggling one.
		Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"operation"})
	if err := reg.Register(durations); err != nil {
		return nil, fmt.Errorf("httptransport: registering metrics: %w", err)
	}
	m := &Metrics{durations: make(map[string]prometheus.Observer, 2)}
	for _, op := range []string{opCheck, opPeek} {
		m.durations[op] = durations.WithLabelValues(op)
	}
	return m, nil
}

// ObserveDecision implements DecisionTimer.
func (m *Metrics) ObserveDecision(operation string, d time.Duration) {
	if o, ok := m.durations[operation]; ok {
		o.Observe(d.Seconds())
	}
}

// timeDecisions times every request to a decision route, from before the
// caller is identified to the handler's return. The instance clock is fine
// here: it measures request duration, never bucket state (ADR 0002).
func timeDecisions(t DecisionTimer, operation string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if t == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			t.ObserveDecision(operation, time.Since(start))
		})
	}
}
