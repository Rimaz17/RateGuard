package cache_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Rimaz17/RateGuard/internal/cache"
)

func TestMetricsCountHitsAndMisses(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := cache.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	src := newSource("a", "1", "b", "2")
	c := cache.New(10*time.Second, time.Second, clk.Now, src.load, cache.WithObserver(m.Observer("api_key")))
	m.Observer("policy") // a cache nobody has used yet still shows, at zero

	mustGet(t, c, "a") // miss
	mustGet(t, c, "a") // hit
	mustGet(t, c, "a") // hit
	mustGet(t, c, "b") // miss
	clk.Advance(10 * time.Second)
	mustGet(t, c, "a") // expired: miss
	c.Invalidate("b")
	mustGet(t, c, "b") // invalidated: miss

	want := `
# HELP rateguard_cache_lookups_total Lookups in the in-process caches on the decision path, by cache and result (hit or miss).
# TYPE rateguard_cache_lookups_total counter
rateguard_cache_lookups_total{cache="api_key",result="hit"} 2
rateguard_cache_lookups_total{cache="api_key",result="miss"} 4
rateguard_cache_lookups_total{cache="policy",result="hit"} 0
rateguard_cache_lookups_total{cache="policy",result="miss"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestNewMetricsRefusesADuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := cache.NewMetrics(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.NewMetrics(reg); err == nil {
		t.Error("registering the metrics twice succeeded, want an error")
	}
}
