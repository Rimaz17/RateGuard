//go:build integration

package integration

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/limiter"
	"github.com/Rimaz17/RateGuard/internal/policy"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// node is one RateGuard process as main wires it: the whole router, with
// decisions on Redis behind the circuit breaker, and its own metrics.
type node struct {
	srv *httptest.Server
}

type nodeOptions struct {
	redisURL  string
	instances int
	timeout   time.Duration
	failures  uint32
	cooldown  time.Duration

	// memoryOnly decides from this process's memory alone, as an
	// in-process limiter would: the counter-example to sharing Redis.
	memoryOnly bool
}

func newNode(t *testing.T, pool *pgxpool.Pool, o nodeOptions) *node {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	rdb := newRedisClient(t, o.redisURL)
	clientStore := postgres.NewClients(pool)
	policyStore := postgres.NewPolicies(pool)

	resolver := policy.NewResolver(clientStore, policyStore, policy.WithTTL(time.Hour))
	keys := client.NewKeyCache(postgres.NewAPIKeys(pool), time.Hour)
	caches := policy.Invalidators{resolver, policy.ClientChanges(keys)}
	broadcaster := policy.NewBroadcaster(rdb, caches, logger)
	runSubscriber(t, rdb, &observed{inner: caches},
		policy.WithBackoff(10*time.Millisecond, 100*time.Millisecond))
	clients := client.New(clientStore, postgres.NewAPIKeys(pool), broadcaster)
	policies := policy.New(policyStore, postgres.NewEndpointRules(pool), clientStore, broadcaster)

	registry := prometheus.NewRegistry()
	metrics, err := limiter.NewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	var primary limiter.Store = limiter.NewRedis(rdb)
	if o.memoryOnly {
		primary = limiter.NewMemory(nil)
	}
	buckets, err := limiter.NewResilient(primary, limiter.NewMemory(nil), limiter.ResilientConfig{
		Mode:             limiter.FailLocal,
		InstanceCount:    o.instances,
		Timeout:          o.timeout,
		FailureThreshold: o.failures,
		Cooldown:         o.cooldown,
		Observer:         metrics,
		Logger:           logger,
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:           logger,
		ReadinessTimeout: time.Second,
		Metrics:          promhttp.HandlerFor(registry, promhttp.HandlerOpts{}),
		AdminToken:       testAdminToken,
		Clients:          clients,
		Keys:             clients,
		Policies:         policies,
		Rules:            policies,
		Assigner:         policies,
		Decider:          limiter.New(resolver, buckets),
		Authenticator:    client.NewAuthenticator(keys),
	}))
	t.Cleanup(srv.Close)
	return &node{srv: srv}
}

// metric scrapes the node's /metrics and returns one sample's value.
func (n *node) metric(t *testing.T, series string) float64 {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, n.srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := n.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if value, ok := strings.CutPrefix(sc.Text(), series+" "); ok {
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatalf("metric %s = %q: %v", series, value, err)
			}
			return v
		}
	}
	t.Fatalf("metric %s not found", series)
	return 0
}

// loadBalancer spreads requests over the nodes in turn, as a round-robin
// load balancer in front of RateGuard would, and counts what each served.
type loadBalancer struct {
	srv    *httptest.Server
	served []atomic.Int64
}

func newLoadBalancer(t *testing.T, nodes ...*node) *loadBalancer {
	t.Helper()
	lb := &loadBalancer{served: make([]atomic.Int64, len(nodes))}
	targets := make([]*url.URL, len(nodes))
	for i, n := range nodes {
		u, err := url.Parse(n.srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		targets[i] = u
	}
	var next atomic.Uint64
	lb.srv = httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			i := int((next.Add(1) - 1) % uint64(len(targets)))
			lb.served[i].Add(1)
			pr.SetURL(targets[i])
			pr.SetXForwarded()
		},
	})
	t.Cleanup(lb.srv.Close)
	return lb
}

// check sends one decision request through the load balancer and returns
// its status code.
func (lb *loadBalancer) check(t *testing.T, key string) int {
	t.Helper()
	// This runs on burst's workers, so it reports with Errorf, never Fatal.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, lb.srv.URL+"/v1/check",
		strings.NewReader(`{"method":"GET","path":"/api/products"}`))
	if err != nil {
		t.Errorf("building request: %v", err)
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := lb.srv.Client().Do(req)
	if err != nil {
		t.Errorf("POST /v1/check: %v", err)
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// burst sends n decision requests from workers goroutines and counts the
// statuses.
func (lb *loadBalancer) burst(t *testing.T, key string, n, workers int) map[int]int {
	t.Helper()
	var (
		mu     sync.Mutex
		counts = map[int]int{}
		jobs   = make(chan struct{}, n)
		wg     sync.WaitGroup
	)
	for range n {
		jobs <- struct{}{}
	}
	close(jobs)
	for range workers {
		wg.Go(func() {
			for range jobs {
				code := lb.check(t, key)
				mu.Lock()
				counts[code]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return counts
}

// planOn creates a default policy of capacity requests an hour, so refill
// during a test is negligible, and registers a client on it.
func planOn(t *testing.T, admin *httptest.Server, capacity int) string {
	t.Helper()
	created := adminJSON(t, admin, http.MethodPost, "/v1/policies",
		fmt.Sprintf(`{"name":"Hourly %d","capacity":%d,"window_seconds":3600,"default":true}`, capacity, capacity), http.StatusCreated)
	policyID, _ := created["id"].(string)
	_, key := registerOn(t, admin, "Customer App", policyID)
	return key
}

// TestThreeInstancesEnforceOneSharedLimit is phase 5's headline demo: three
// instances behind a load balancer, one client with a limit of 100, and 300
// concurrent requests. Exactly 100 are allowed, although each instance
// served about 100 of them. The same setup with per-instance memory, the
// limiter RateGuard replaces, lets most of the 300 through.
func TestThreeInstancesEnforceOneSharedLimit(t *testing.T) {
	_, redisURL := startRedis(t.Context(), t)

	for _, tt := range []struct {
		name       string
		memoryOnly bool
	}{
		{"shared through redis", false},
		{"counter-example: per-instance memory", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pool := migratedPool(t)
			o := nodeOptions{redisURL: redisURL, instances: 3, timeout: time.Second, failures: 5, cooldown: time.Second, memoryOnly: tt.memoryOnly}
			a, b, c := newNode(t, pool, o), newNode(t, pool, o), newNode(t, pool, o)
			lb := newLoadBalancer(t, a, b, c)
			key := planOn(t, a.srv, 100)

			start := time.Now()
			counts := lb.burst(t, key, 300, 30)
			elapsed := time.Since(start)
			served := []int64{lb.served[0].Load(), lb.served[1].Load(), lb.served[2].Load()}
			t.Logf("300 requests in %v: %d allowed, %d refused; instances served %v", elapsed.Round(time.Millisecond), counts[200], counts[429], served)

			if counts[200]+counts[429] != 300 {
				t.Fatalf("statuses = %v, want only 200 and 429", counts)
			}
			for i, n := range served {
				if n < 90 {
					t.Errorf("instance %d served %d requests; the load balancer should spread them evenly", i, n)
				}
			}
			if tt.memoryOnly {
				if counts[200] <= 100 {
					t.Errorf("per-instance memory allowed %d; the counter-example should exceed the shared limit", counts[200])
				}
				return
			}
			// One token returns every 36 seconds; the burst takes far less.
			if counts[200] != 100 || elapsed > 36*time.Second {
				t.Errorf("allowed %d of 300 in %v, want exactly the shared limit of 100", counts[200], elapsed)
			}
		})
	}
}

// TestKillingRedisDegradesAndRecovers is the second half of phase 5's
// done-when (PDF 8.1). With Redis gone, every decision is still made, by
// each instance's local bucket of capacity / 3, and the degradation shows
// in the metrics. When Redis returns, the breakers close and Redis's
// buckets decide again, where they were left.
func TestKillingRedisDegradesAndRecovers(t *testing.T) {
	pool := migratedPool(t)
	_, directURL := startRedis(t.Context(), t)
	proxy, redisURL := startRedisProxy(t, directURL)

	o := nodeOptions{redisURL: redisURL, instances: 3, timeout: 100 * time.Millisecond, failures: 3, cooldown: 300 * time.Millisecond}
	nodes := []*node{newNode(t, pool, o), newNode(t, pool, o), newNode(t, pool, o)}
	lb := newLoadBalancer(t, nodes...)
	key := planOn(t, nodes[0].srv, 30)

	// Healthy: 15 of the shared 30 are used.
	if counts := lb.burst(t, key, 15, 1); counts[200] != 15 {
		t.Fatalf("before the outage statuses = %v, want 15 allowed", counts)
	}

	proxy.Kill()
	// Sequential, so the round robin gives each instance exactly 20. Each
	// one's local bucket holds 30 / 3 = 10.
	counts := lb.burst(t, key, 60, 1)
	t.Logf("with redis down: %v", counts)
	if counts[200]+counts[429] != 60 {
		t.Fatalf("with Redis down statuses = %v; every request must still be decided, as 200 or 429", counts)
	}
	if counts[200] != 30 {
		t.Errorf("with Redis down %d were allowed, want 30: 10 from each instance's local bucket, never unlimited", counts[200])
	}
	var degraded float64
	for i, n := range nodes {
		degraded += n.metric(t, `rateguard_degraded_decisions_total{mode="local"}`)
		if s := n.metric(t, "rateguard_redis_breaker_state"); s != float64(limiter.BreakerOpen) {
			t.Errorf("instance %d breaker state = %v, want open (2)", i, s)
		}
	}
	if degraded != 60 {
		t.Errorf("degraded decisions across instances = %v, want all 60", degraded)
	}

	proxy.Restore(t)
	time.Sleep(o.cooldown + 100*time.Millisecond)
	// Each instance's first request is its probe. Redis still holds the 15
	// tokens left before the outage.
	counts = lb.burst(t, key, 21, 1)
	t.Logf("after redis returned: %v", counts)
	if counts[200] != 15 || counts[429] != 6 {
		t.Errorf("after recovery statuses = %v, want Redis's remaining 15 allowed and 6 refused", counts)
	}
	var after float64
	for i, n := range nodes {
		after += n.metric(t, `rateguard_degraded_decisions_total{mode="local"}`)
		if s := n.metric(t, "rateguard_redis_breaker_state"); s != float64(limiter.BreakerClosed) {
			t.Errorf("instance %d breaker state = %v after recovery, want closed (0)", i, s)
		}
	}
	if after != degraded {
		t.Errorf("degraded decisions rose from %v to %v after recovery; Redis should decide again", degraded, after)
	}
}
