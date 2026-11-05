package analytics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics exports what the usage pipeline loses to Prometheus. It satisfies
// Observer.
type Metrics struct {
	dropped prometheus.Counter
}

var _ Observer = (*Metrics)(nil)

// NewMetrics creates the usage pipeline's metrics and registers them with
// reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	dropped := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "rateguard_usage_dropped_total",
		Help: "Checks whose usage was lost before reaching Postgres, so analytics undercount them (ADR 0018).",
	})
	if err := reg.Register(dropped); err != nil {
		return nil, fmt.Errorf("analytics: registering metrics: %w", err)
	}
	return &Metrics{dropped: dropped}, nil
}

// UsageDropped implements Observer.
func (m *Metrics) UsageDropped(checks int64) {
	if checks > 0 {
		m.dropped.Add(float64(checks))
	}
}
