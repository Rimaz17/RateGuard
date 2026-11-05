package analytics_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Rimaz17/RateGuard/internal/analytics"
)

func TestMetricsCountDroppedChecks(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := analytics.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	rec := analytics.NewRecorder(analytics.RecorderConfig{MaxSlots: 1, Observer: m})
	rec.Record(clientA, true)
	rec.Record(clientB, true) // no room
	m.UsageDropped(4)
	m.UsageDropped(0)

	want := `
# HELP rateguard_usage_dropped_total Checks whose usage was lost before reaching Postgres, so analytics undercount them (ADR 0018).
# TYPE rateguard_usage_dropped_total counter
rateguard_usage_dropped_total 5
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}
