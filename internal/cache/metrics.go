package cache

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics counts cache lookups by cache and result, for the hit ratio
// (ADR 0019):
//
//	sum by (cache) (rate(rateguard_cache_lookups_total{result="hit"}[5m]))
//	  / sum by (cache) (rate(rateguard_cache_lookups_total[5m]))
type Metrics struct {
	lookups *prometheus.CounterVec
}

// NewMetrics creates the cache metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	lookups := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rateguard_cache_lookups_total",
		Help: "Lookups in the in-process caches on the decision path, by cache and result (hit or miss).",
	}, []string{"cache", "result"})
	if err := reg.Register(lookups); err != nil {
		return nil, fmt.Errorf("cache: registering metrics: %w", err)
	}
	return &Metrics{lookups: lookups}, nil
}

// Observer returns an Observer that counts the lookups of the cache called
// name. name becomes a label value, so it must be one of a fixed few,
// never anything a caller chooses. Both series exist from this call on.
func (m *Metrics) Observer(name string) Observer {
	return counted{
		hit:  m.lookups.WithLabelValues(name, "hit"),
		miss: m.lookups.WithLabelValues(name, "miss"),
	}
}

type counted struct {
	hit, miss prometheus.Counter
}

func (c counted) Lookup(hit bool) {
	if hit {
		c.hit.Inc()
	} else {
		c.miss.Inc()
	}
}
