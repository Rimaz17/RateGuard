package analytics

import (
	"context"
	"log/slog"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// DefaultRollupInterval is how often a Rollup moves counts from Redis into
// Postgres. Analytics lag real time by about this plus the flush interval.
const DefaultRollupInterval = 10 * time.Second

// minutesPerRollup bounds how many minutes one rollup drains, so a backlog
// left by a long Postgres outage is worked off in steps.
const minutesPerRollup = 120

// rollupTimeout bounds each step of a rollup: draining Redis, writing the
// counts to Postgres, and putting them back into Redis if that fails.
// Drained counts exist only in this process, so the last two run even while
// the instance is shutting down.
const rollupTimeout = 5 * time.Second

// Pending is where flushed counts wait for the rollup: Redis.
// *RedisPending satisfies it.
type Pending interface {
	Sink

	// Drain removes and returns the counts of up to maxMinutes minutes,
	// oldest first. Counts drained before an error are returned with it.
	Drain(ctx context.Context, maxMinutes int) ([]domain.UsageCount, error)
}

// Store adds counts to the usage_rollups table. *postgres.Usage satisfies
// it.
type Store interface {
	AddUsage(ctx context.Context, counts []domain.UsageCount) error
}

// Rollup moves counts from Pending into the Store at a fixed interval. Every
// instance runs one: a drain is atomic in Redis, so two rollups never take
// the same counts, and the store adds rather than overwrites.
type Rollup struct {
	pending  Pending
	store    Store
	every    time.Duration
	logger   *slog.Logger
	observer Observer

	failing bool // touched only by Run's goroutine
}

// NewRollup returns a Rollup that moves counts from pending into store every
// interval. observer is told about counts lost; it may be nil.
func NewRollup(pending Pending, store Store, every time.Duration, logger *slog.Logger, observer Observer) *Rollup {
	if observer == nil {
		observer = nopObserver{}
	}
	return &Rollup{pending: pending, store: store, every: every, logger: logger, observer: observer}
}

// Run rolls up every interval until ctx ends.
func (r *Rollup) Run(ctx context.Context) {
	t := time.NewTicker(r.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Roll(ctx)
		}
	}
}

// Roll drains what is pending and stores it now. If the store fails, the
// counts go back into Pending for a later rollup. Run calls it every
// interval; it must not be called while Run is running.
func (r *Rollup) Roll(ctx context.Context) {
	dctx, dcancel := context.WithTimeout(ctx, rollupTimeout)
	counts, drainErr := r.pending.Drain(dctx, minutesPerRollup)
	dcancel()
	if drainErr != nil && ctx.Err() == nil {
		r.logger.WarnContext(ctx, "draining usage from redis failed",
			slog.Int("counts_drained", len(counts)),
			slog.Any("error", drainErr))
	}
	if len(counts) == 0 {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollupTimeout)
	defer cancel()
	err := r.store.AddUsage(wctx, counts)
	if err == nil {
		if r.failing {
			r.failing = false
			r.logger.InfoContext(ctx, "storing usage in postgres recovered")
		}
		return
	}

	unsent, putErr := r.pending.Add(wctx, counts)
	if putErr != nil {
		lost := checks(unsent)
		r.observer.UsageDropped(lost)
		r.logger.ErrorContext(ctx, "usage lost: storing it in postgres failed, and so did putting it back in redis",
			slog.Int64("checks_lost", lost),
			slog.Any("error", err),
			slog.Any("put_back_error", putErr))
		return
	}
	if !r.failing {
		r.failing = true
		r.logger.WarnContext(ctx, "storing usage in postgres failed; counts kept in redis for the next rollup",
			slog.Any("error", err))
	}
}
