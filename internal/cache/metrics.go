package cache

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics counts cache lookups by cache and result, for the hit ratio
// (ADR 0019) and for how often a cache answers from a stale entry because
// Postgres cannot be reached (ADR 0021):
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
		Help: "Lookups in the in-process caches on the decision path, by cache and result (hit, miss, or stale while the database is unreachable).",
	}, []string{"cache", "result"})
	if err := reg.Register(lookups); err != nil {
		return nil, fmt.Errorf("cache: registering metrics: %w", err)
	}
	return &Metrics{lookups: lookups}, nil
}

// Observer returns an Observer that counts the lookups of the cache called
// name. name becomes a label value, so it must be one of a fixed few,
// never anything a caller chooses. Every result's series exists from this
// call on.
func (m *Metrics) Observer(name string) Observer {
	return counted{
		hit:   m.lookups.WithLabelValues(name, Hit.String()),
		miss:  m.lookups.WithLabelValues(name, Miss.String()),
		stale: m.lookups.WithLabelValues(name, Stale.String()),
	}
}

type counted struct {
	hit, miss, stale prometheus.Counter
}

func (c counted) Lookup(r Result) {
	switch r {
	case Hit:
		c.hit.Inc()
	case Stale:
		c.stale.Inc()
	default:
		c.miss.Inc()
	}
}
