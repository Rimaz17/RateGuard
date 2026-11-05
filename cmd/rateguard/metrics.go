package main

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/Rimaz17/RateGuard/internal/analytics"
	"github.com/Rimaz17/RateGuard/internal/cache"
	"github.com/Rimaz17/RateGuard/internal/limiter"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// cacheAPIKey names the API key cache in rateguard_cache_lookups_total. The
// resolver names its own caches (policy.CacheClientPolicy and the rest).
const cacheAPIKey = "api_key"

// instruments are the Prometheus metrics each package exports (ADR 0014,
// ADR 0019).
type instruments struct {
	limiter *limiter.Metrics
	cache   *cache.Metrics
	http    *httptransport.Metrics
	usage   *analytics.Metrics
}

// newInstruments creates every metric in a registry of the service's own,
// never the global one, along with the Go runtime and process metrics.
func newInstruments() (*prometheus.Registry, instruments, error) {
	registry := prometheus.NewRegistry()
	for _, c := range []prometheus.Collector{
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	} {
		if err := registry.Register(c); err != nil {
			return nil, instruments{}, fmt.Errorf("registering runtime metrics: %w", err)
		}
	}
	var (
		m   instruments
		err error
	)
	if m.limiter, err = limiter.NewMetrics(registry); err != nil {
		return nil, instruments{}, err
	}
	if m.cache, err = cache.NewMetrics(registry); err != nil {
		return nil, instruments{}, err
	}
	if m.http, err = httptransport.NewMetrics(registry); err != nil {
		return nil, instruments{}, err
	}
	if m.usage, err = analytics.NewMetrics(registry); err != nil {
		return nil, instruments{}, err
	}
	return registry, m, nil
}
