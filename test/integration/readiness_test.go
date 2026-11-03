//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// redisImage must match docker-compose.yml.
const redisImage = "redis:8.10.3-alpine"

func startRedis(ctx context.Context, t *testing.T) (*testcontainers.DockerContainer, string) {
	t.Helper()
	ctr, err := testcontainers.Run(ctx, redisImage,
		testcontainers.WithCmd("redis-server", "--save", ""),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategyAndDeadline(time.Minute,
			wait.ForLog("Ready to accept connections"),
			wait.ForListeningPort("6379/tcp"),
		),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("running %s: %v", redisImage, err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		t.Fatalf("resolving redis endpoint: %v", err)
	}
	return ctr, "redis://" + endpoint + "/0"
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func probeReadyz(t *testing.T, h http.Handler) (int, readyResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	var body readyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding /readyz body %q: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func expectReadyz(t *testing.T, h http.Handler, wantCode int, wantChecks map[string]string) {
	t.Helper()
	code, body := probeReadyz(t, h)
	if code != wantCode || fmt.Sprint(body.Checks) != fmt.Sprint(wantChecks) {
		t.Fatalf("/readyz = %d %v, want %d %v", code, body.Checks, wantCode, wantChecks)
	}
}

// TestReadinessFlipsWhenDependenciesStop wires /readyz to real Postgres and
// Redis containers, as main does, and stops them one at a time. Phase 1 is
// done when readiness flips as Postgres goes away; CI's compose job also
// checks that it flips back once Postgres returns.
func TestReadinessFlipsWhenDependenciesStop(t *testing.T) {
	ctx := t.Context()

	pgCtr, pgURL, err := startPostgres(ctx)
	testcontainers.CleanupContainer(t, pgCtr)
	if err != nil {
		t.Fatalf("starting postgres: %v", err)
	}
	redisCtr, redisURL := startRedis(ctx, t)

	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{URL: pgURL, MaxConns: 2, MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute})
	if err != nil {
		t.Fatalf("creating pool: %v", err)
	}
	t.Cleanup(pool.Close)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parsing redis URL: %v", err)
	}
	opts.ContextTimeoutEnabled = true
	opts.MaxRetries = -1 // Fail fast once the container is gone.
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	h := httptransport.NewRouter(httptransport.Deps{
		Logger: slog.New(slog.DiscardHandler),
		Readiness: []httptransport.ReadinessCheck{
			{Name: "postgres", Check: pool.Ping},
			{Name: "redis", Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
		},
		ReadinessTimeout: 2 * time.Second,
	})

	expectReadyz(t, h, http.StatusOK, map[string]string{"postgres": "ok", "redis": "ok"})

	stopTimeout := 10 * time.Second
	if err := pgCtr.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stopping postgres: %v", err)
	}
	expectReadyz(t, h, http.StatusServiceUnavailable, map[string]string{"postgres": "unavailable", "redis": "ok"})

	if err := redisCtr.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stopping redis: %v", err)
	}
	expectReadyz(t, h, http.StatusServiceUnavailable, map[string]string{"postgres": "unavailable", "redis": "unavailable"})
}
