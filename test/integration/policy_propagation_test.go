//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestPlanChangeReachesEveryInstanceInMilliseconds is the second half of
// phase 3's done-when. Two instances share Postgres and Redis. Each change
// goes through instance A's admin API and is then polled for on instance
// B, whose cache TTL is an hour, so only pub/sub can explain B seeing it.
func TestPlanChangeReachesEveryInstanceInMilliseconds(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	a := newInstance(t, pool, redisURL)
	b := newInstance(t, pool, redisURL)

	ids := setUpPlans(t, a.admin)
	customerID, customerKey := registerOn(t, a.admin, "Customer App", ids["Free"])
	_, merchantKey := registerOn(t, a.admin, "Merchant App", ids["Premium"])

	// Warm B's cache with the current state of everything the changes
	// below touch.
	for _, warm := range []struct{ key, method, path string }{
		{customerKey, http.MethodGet, "/api/products"},
		{merchantKey, http.MethodGet, "/api/products"},
		{merchantKey, http.MethodGet, "/api/reports/1"},
		{"", http.MethodGet, "/"},
	} {
		resolveVia(t, b, warm.key, warm.method, warm.path)
	}

	const budget = time.Second // generous for CI; the logged figures are the result
	changes := []struct {
		name         string
		apply        func()
		key          string
		method, path string
		want         resolvedWire
	}{
		{
			name: "client upgraded from Free to Premium",
			apply: func() {
				adminJSON(t, a.admin, http.MethodPut, "/v1/clients/"+customerID+"/policy", `{"policy_id":"`+ids["Premium"]+`"}`, http.StatusOK)
			},
			key: customerKey, method: http.MethodGet, path: "/api/products",
			want: resolvedWire{"Premium", false, 1000, 60},
		},
		{
			name: "Premium capacity raised",
			apply: func() {
				adminJSON(t, a.admin, http.MethodPatch, "/v1/policies/"+ids["Premium"], `{"capacity":1200}`, http.StatusOK)
			},
			key: merchantKey, method: http.MethodGet, path: "/api/products",
			want: resolvedWire{"Premium", false, 1200, 60},
		},
		{
			name: "endpoint rule added to Premium",
			apply: func() {
				adminJSON(t, a.admin, http.MethodPost, "/v1/policies/"+ids["Premium"]+"/rules",
					`{"method":"GET","path_pattern":"/api/reports/*","capacity":2,"window_seconds":60}`, http.StatusCreated)
			},
			key: merchantKey, method: http.MethodGet, path: "/api/reports/1",
			want: resolvedWire{"Premium", true, 2, 60},
		},
		{
			name: "default moved from Free to Enterprise",
			apply: func() {
				adminJSON(t, a.admin, http.MethodPatch, "/v1/policies/"+ids["Enterprise"], `{"default":true}`, http.StatusOK)
			},
			key: "", method: http.MethodGet, path: "/",
			want: resolvedWire{"Enterprise", false, 5000, 60},
		},
	}

	var report []string
	for _, c := range changes {
		if got := resolveVia(t, b, c.key, c.method, c.path); got == c.want {
			t.Fatalf("%s: instance B already resolves %v before the change; the test proves nothing", c.name, got)
		}
		start := time.Now()
		c.apply()
		applied := time.Since(start)
		var got resolvedWire
		for {
			got = resolveVia(t, b, c.key, c.method, c.path)
			if got == c.want || time.Since(start) > budget {
				break
			}
			time.Sleep(time.Millisecond)
		}
		visible := time.Since(start)
		if got != c.want {
			t.Errorf("%s: instance B still resolves %v after %v, want %v", c.name, got, visible, c.want)
			continue
		}
		report = append(report, fmt.Sprintf("%s: visible on B %v after the admin call started (the call took %v)", c.name, visible.Round(time.Microsecond), applied.Round(time.Microsecond)))
	}
	t.Log("plan change propagation:\n  " + strings.Join(report, "\n  "))
}
