package limiter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

func TestMetricsStartAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := limiter.NewMetrics(reg); err != nil {
		t.Fatal(err)
	}
	want := `
# HELP rateguard_degraded_decisions_total Rate-limit decisions made without Redis, by failure mode (ADR 0001).
# TYPE rateguard_degraded_decisions_total counter
rateguard_degraded_decisions_total{mode="closed"} 0
rateguard_degraded_decisions_total{mode="local"} 0
rateguard_degraded_decisions_total{mode="open"} 0
# HELP rateguard_redis_breaker_state State of the circuit breaker around Redis: 0 closed, 1 half-open, 2 open.
# TYPE rateguard_redis_breaker_state gauge
rateguard_redis_breaker_state 0
# HELP rateguard_redis_errors_total Bucket operations in Redis that failed or timed out.
# TYPE rateguard_redis_errors_total counter
rateguard_redis_errors_total 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestMetricsFollowTheBreaker(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := limiter.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	primary := newFlaky()
	primary.set(errRedisDown, false)
	s, err := limiter.NewResilient(primary, limiter.NewMemory(nil), limiter.ResilientConfig{
		Mode: limiter.FailLocal, InstanceCount: 2, Timeout: time.Second, FailureThreshold: 2, Cooldown: time.Hour, Observer: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		mustTake(t, s, "k", slowly)
	}

	want := `
# HELP rateguard_degraded_decisions_total Rate-limit decisions made without Redis, by failure mode (ADR 0001).
# TYPE rateguard_degraded_decisions_total counter
rateguard_degraded_decisions_total{mode="closed"} 0
rateguard_degraded_decisions_total{mode="local"} 5
rateguard_degraded_decisions_total{mode="open"} 0
# HELP rateguard_redis_breaker_state State of the circuit breaker around Redis: 0 closed, 1 half-open, 2 open.
# TYPE rateguard_redis_breaker_state gauge
rateguard_redis_breaker_state 2
# HELP rateguard_redis_errors_total Bucket operations in Redis that failed or timed out.
# TYPE rateguard_redis_errors_total counter
rateguard_redis_errors_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestNewMetricsRefusesADuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := limiter.NewMetrics(reg); err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.NewMetrics(reg); err == nil {
		t.Error("registering the metrics twice succeeded, want an error")
	}
}
