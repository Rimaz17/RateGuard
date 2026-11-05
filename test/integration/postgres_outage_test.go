//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

// TestDecisionsSurviveAPostgresOutage cuts one instance off from Postgres
// for many times the cache TTL. A caller whose key and plan were cached
// keeps getting decisions, answered from the stale entries, and the stale
// lookups show in the metrics (ADR 0021). Before that, every check failed
// with 500 as soon as the entries expired. When Postgres returns, the
// caches reload and stop answering stale.
func TestDecisionsSurviveAPostgresOutage(t *testing.T) {
	dbURL := newDatabase(t)
	if err := newMigrator(t, dbURL).Up(t.Context()); err != nil {
		t.Fatalf("migrating up: %v", err)
	}
	// The proxy forwards any TCP address: killing it is, to this pool,
	// Postgres dying.
	proxy, proxiedURL := startRedisProxy(t, dbURL)
	pool, err := postgres.NewPool(t.Context(), postgres.PoolConfig{
		URL:             proxiedURL,
		MaxConns:        4,
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: time.Minute,
	})
	if err != nil {
		t.Fatalf("creating pool: %v", err)
	}
	t.Cleanup(pool.Close)
	_, redisURL := startRedis(t.Context(), t)

	const ttl = 200 * time.Millisecond
	n := newNode(t, pool, nodeOptions{redisURL: redisURL, instances: 1, timeout: time.Second, failures: 5, cooldown: time.Second, cacheTTL: ttl})
	lb := newLoadBalancer(t, n)
	key := planOn(t, n.srv, 1000)
	if code := lb.check(t, key); code != http.StatusOK {
		t.Fatalf("check before the outage = %d, want 200", code)
	}

	proxy.Kill()
	time.Sleep(5 * ttl)
	start := time.Now()
	for i := range 20 {
		if code := lb.check(t, key); code != http.StatusOK {
			t.Fatalf("check %d with Postgres down for %v = %d, want 200 from the cached key and plan", i, 5*ttl, code)
		}
	}
	t.Logf("20 checks with Postgres down took %v", time.Since(start).Round(time.Millisecond))
	stale := `rateguard_cache_lookups_total{cache="api_key",result="stale"}`
	if s := n.metric(t, stale); s == 0 {
		t.Errorf("%s = 0 after checks answered during the outage, want them counted", stale)
	}

	proxy.Restore(t)
	if code := lb.check(t, key); code != http.StatusOK {
		t.Fatalf("check after Postgres returned = %d, want 200", code)
	}
	// Background retries run about once a second. The first one or two may
	// still fail while the pool replaces connections the outage broke, so
	// wait for a retry interval in which nothing is answered stale.
	deadline := time.Now().Add(10 * time.Second)
	for round := 1; ; round++ {
		before := n.metric(t, stale)
		for range 8 { // 8 × ttl spans more than one retry interval
			if code := lb.check(t, key); code != http.StatusOK {
				t.Fatalf("check after recovery = %d, want 200", code)
			}
			time.Sleep(ttl)
		}
		if n.metric(t, stale) == before {
			t.Logf("caches reloaded within %d rounds after Postgres returned", round)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still rising 10s after Postgres returned; the caches should reload", stale)
		}
	}
}
