package limiter

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics exports what the limiter sees to Prometheus: check outcomes, and
// what the circuit breaker sees (ADR 0014, ADR 0019). It satisfies Observer
// and OutcomeCounter. Its labels are fixed and few: never a client, a key
// or an address (CLAUDE.md section 11).
type Metrics struct {
	decisions    map[Outcome]prometheus.Counter
	degraded     map[FailureMode]prometheus.Counter
	redisErrors  prometheus.Counter
	breakerState prometheus.Gauge
}

var (
	_ Observer       = (*Metrics)(nil)
	_ OutcomeCounter = (*Metrics)(nil)
)

// NewMetrics creates the limiter's metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	decisions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rateguard_decisions_total",
		Help: "Checks on /v1/check by outcome: allowed, rejected, no_policy or error (ADR 0019).",
	}, []string{"outcome"})
	degraded := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rateguard_degraded_decisions_total",
		Help: "Rate-limit decisions made without Redis, by failure mode (ADR 0001).",
	}, []string{"mode"})
	redisErrors := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rateguard_redis_errors_total",
		Help: "Bucket operations in Redis that failed or timed out.",
	})
	breakerState := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "rateguard_redis_breaker_state",
		Help: "State of the circuit breaker around Redis: 0 closed, 1 half-open, 2 open.",
	})
	for _, c := range []prometheus.Collector{decisions, degraded, redisErrors, breakerState} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("limiter: registering metrics: %w", err)
		}
	}

	// Every label's series exists from the start, at zero, so a dashboard or
	// alert sees "none yet" rather than no data. The counters are looked up
	// once here, not on every decision.
	m := &Metrics{
		decisions:    make(map[Outcome]prometheus.Counter, 4),
		degraded:     make(map[FailureMode]prometheus.Counter, 3),
		redisErrors:  redisErrors,
		breakerState: breakerState,
	}
	for _, o := range []Outcome{OutcomeAllowed, OutcomeRejected, OutcomeNoPolicy, OutcomeError} {
		m.decisions[o] = decisions.WithLabelValues(string(o))
	}
	for _, mode := range []FailureMode{FailLocal, FailOpen, FailClosed} {
		m.degraded[mode] = degraded.WithLabelValues(string(mode))
	}
	return m, nil
}

// Checked implements OutcomeCounter.
func (m *Metrics) Checked(o Outcome) {
	if c, ok := m.decisions[o]; ok {
		c.Inc()
	}
}

// DegradedDecision implements Observer.
func (m *Metrics) DegradedDecision(mode FailureMode) {
	if c, ok := m.degraded[mode]; ok {
		c.Inc()
	}
}

// RedisError implements Observer.
func (m *Metrics) RedisError() { m.redisErrors.Inc() }

// BreakerStateChanged implements Observer.
func (m *Metrics) BreakerStateChanged(s BreakerState) { m.breakerState.Set(float64(s)) }
