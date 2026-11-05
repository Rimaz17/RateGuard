package limiter

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics exports what the circuit breaker sees to Prometheus (ADR 0014).
// It satisfies Observer. Its labels are fixed and few: never a client, a key
// or an address (CLAUDE.md section 11).
type Metrics struct {
	degraded     map[FailureMode]prometheus.Counter
	redisErrors  prometheus.Counter
	breakerState prometheus.Gauge
}

var _ Observer = (*Metrics)(nil)

// NewMetrics creates the limiter's metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
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
	for _, c := range []prometheus.Collector{degraded, redisErrors, breakerState} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("limiter: registering metrics: %w", err)
		}
	}

	// Every mode's series exists from the start, at zero, so a dashboard or
	// alert sees "no degradation" rather than no data. The counters are
	// looked up once here, not on every degraded decision.
	m := &Metrics{
		degraded:     make(map[FailureMode]prometheus.Counter, 3),
		redisErrors:  redisErrors,
		breakerState: breakerState,
	}
	for _, mode := range []FailureMode{FailLocal, FailOpen, FailClosed} {
		m.degraded[mode] = degraded.WithLabelValues(string(mode))
	}
	return m, nil
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
