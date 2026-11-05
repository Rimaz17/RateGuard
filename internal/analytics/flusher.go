package analytics

import (
	"context"
	"log/slog"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// DefaultFlushInterval is how often a Flusher moves counts into Redis, and
// so about how much usage an instance that crashes can lose.
const DefaultFlushInterval = time.Second

// flushTimeout bounds each flush, and the last one on shutdown, so a hung
// Redis cannot stall the flusher.
const flushTimeout = 2 * time.Second

// Sink stores counts where every instance's counts add up: Redis.
// *RedisPending satisfies it.
type Sink interface {
	// Add adds counts to what is stored. On error it returns the counts it
	// did not store, which may be all of them.
	Add(ctx context.Context, counts []domain.UsageCount) (unsent []domain.UsageCount, err error)
}

// Flusher moves a Recorder's counts into a Sink at a fixed interval. Counts
// that fail to flush go back into the Recorder for the next attempt.
type Flusher struct {
	rec      *Recorder
	sink     Sink
	every    time.Duration
	logger   *slog.Logger
	observer Observer

	failing bool // touched only by Run's goroutine
}

// NewFlusher returns a Flusher that moves rec's counts into sink every
// interval. observer is told about counts lost at shutdown; it may be nil.
func NewFlusher(rec *Recorder, sink Sink, every time.Duration, logger *slog.Logger, observer Observer) *Flusher {
	if observer == nil {
		observer = nopObserver{}
	}
	return &Flusher{rec: rec, sink: sink, every: every, logger: logger, observer: observer}
}

// Run flushes every interval until ctx ends, then flushes one last time.
// Cancel ctx only once nothing records any more, after the HTTP servers
// have drained, or the last checks are lost.
func (f *Flusher) Run(ctx context.Context) {
	t := time.NewTicker(f.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			f.final(ctx)
			return
		case <-t.C:
			f.Flush(ctx)
		}
	}
}

// Flush moves everything buffered into the sink now, or back into the
// buffer if the sink fails. Run calls it every interval; it must not be
// called while Run is running.
func (f *Flusher) Flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	counts := f.rec.take()
	if len(counts) == 0 {
		return
	}
	unsent, err := f.sink.Add(ctx, counts)
	if err != nil {
		f.rec.restore(unsent)
		if !f.failing {
			f.failing = true
			f.logger.WarnContext(ctx, "flushing usage to redis failed; holding counts in memory and retrying",
				slog.Int64("checks_held", checks(unsent)),
				slog.Any("error", err))
		}
		return
	}
	if f.failing {
		f.failing = false
		f.logger.InfoContext(ctx, "flushing usage to redis recovered")
	}
}

// final flushes what is left at shutdown. Anything it cannot store is lost.
func (f *Flusher) final(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	defer cancel()
	counts := f.rec.take()
	if len(counts) == 0 {
		return
	}
	unsent, err := f.sink.Add(ctx, counts)
	if err != nil {
		lost := checks(unsent)
		f.observer.UsageDropped(lost)
		f.logger.ErrorContext(ctx, "usage lost at shutdown: the last flush to redis failed",
			slog.Int64("checks_lost", lost),
			slog.Any("error", err))
	}
}
