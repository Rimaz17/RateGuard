package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"

	"github.com/Rimaz17/RateGuard/internal/client"
	"github.com/Rimaz17/RateGuard/internal/config"
	"github.com/Rimaz17/RateGuard/internal/policy"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// serve runs the HTTP server until SIGINT or SIGTERM, then drains it.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Neither constructor connects: the service starts even while a
	// dependency is down, and /readyz reports it.
	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		URL:             cfg.Database.URL.Value(),
		MaxConns:        cfg.Database.MaxConns,
		MaxConnLifetime: cfg.Database.MaxConnLifetime,
		MaxConnIdleTime: cfg.Database.MaxConnIdleTime,
	})
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	// go-redis logs through one process-wide logger, plain text to stderr
	// by default. Route it into the JSON log; main is the only place that
	// may touch process-wide state.
	redis.SetLogger(redisLogger{logger})
	rdb, err := newRedisClient(cfg.Redis.URL.Value())
	if err != nil {
		return err
	}
	defer func() {
		if err := rdb.Close(); err != nil {
			logger.Warn("closing redis client", slog.Any("error", err))
		}
	}()

	clientStore := postgres.NewClients(pool)
	policyStore := postgres.NewPolicies(pool)

	// The resolver caches what decisions need (ADR 0005). Writes through
	// the policy and client services drop changes from it directly and
	// publish them for the other instances; the subscriber applies theirs
	// here.
	resolver := policy.NewResolver(clientStore, policyStore)
	broadcaster := policy.NewBroadcaster(rdb, resolver, logger)
	clients := client.New(clientStore, postgres.NewAPIKeys(pool), broadcaster)
	policies := policy.New(policyStore, postgres.NewEndpointRules(pool), clientStore, broadcaster)
	// The subscriber stops on SIGINT/SIGTERM like everything else, and on
	// any early return; the deferred stop runs before the Redis client
	// closes, so it never outlives the connection it uses.
	defer background(ctx, policy.NewSubscriber(rdb, resolver, logger).Run)()

	router := httptransport.NewRouter(httptransport.Deps{
		Logger: logger,
		Readiness: []httptransport.ReadinessCheck{
			{Name: "postgres", Check: pool.Ping},
			{Name: "redis", Check: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
		},
		ReadinessTimeout: cfg.ReadinessTimeout,
		AdminToken:       cfg.AdminToken.Value(),
		Clients:          clients,
		Keys:             clients,
		Policies:         policies,
		Rules:            policies,
		Assigner:         policies,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", slog.String("timeout", cfg.ShutdownTimeout.String()))
	// The signal context is already cancelled, so the drain deadline needs a
	// fresh root; this is main, the one place that may create one.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// background runs loop on its own goroutine until ctx ends or the returned
// stop function is called. stop cancels the loop and waits for it to
// return, so a deferred stop guarantees the goroutine has ended.
func background(ctx context.Context, loop func(context.Context)) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// newRedisClient builds a client from a redis:// or rediss:// URL without
// connecting. The parser's error can quote the URL, password included, so
// it is replaced with a fixed message.
func newRedisClient(url string) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("RG_REDIS_URL is not a valid redis:// or rediss:// URL")
	}
	// Without this, go-redis applies its own read and write timeouts and
	// ignores context deadlines, so neither the readiness timeout nor the
	// decision timeout would bind.
	opts.ContextTimeoutEnabled = true
	// A retried bucket script whose first reply was lost would take a second
	// token, and retries delay the circuit breaker. A failed call is a
	// failure (ADR 0013); this overrides any max_retries in the URL.
	opts.MaxRetries = -1
	return redis.NewClient(opts), nil
}

// redisLogger adapts slog to go-redis's Printf-style logger. go-redis only
// logs trouble (failed dials, broken connections), so it maps to WARN.
type redisLogger struct {
	logger *slog.Logger
}

func (l redisLogger) Printf(ctx context.Context, format string, v ...any) {
	l.logger.WarnContext(ctx, "redis client", slog.String("detail", fmt.Sprintf(format, v...)))
}
