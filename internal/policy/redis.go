package policy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// Channel is the Redis pub/sub channel that carries changes between
// instances (ADR 0005).
const Channel = "rg:policy:invalidate"

// Invalidator is what a change notice acts on: the local Resolver.
type Invalidator interface {
	Invalidate(c Change)
	Flush()
}

// publishTimeout bounds a publish, which runs after the admin write has
// committed and must not hold up its response for long.
const publishTimeout = 2 * time.Second

// Broadcaster announces changes to every instance. It drops the change from
// this instance's cache directly, then publishes it on Channel for the
// others, so the instance that took the write is never stale even when
// Redis is down.
type Broadcaster struct {
	rdb    *redis.Client
	local  Invalidator
	logger *slog.Logger
}

// NewBroadcaster returns a Broadcaster that publishes on rdb and
// invalidates local directly.
func NewBroadcaster(rdb *redis.Client, local Invalidator, logger *slog.Logger) *Broadcaster {
	return &Broadcaster{rdb: rdb, local: local, logger: logger}
}

// Notify implements Notifier. A failed publish is logged at WARN and
// otherwise ignored: the write has already committed, and other instances
// catch up within the cache TTL.
func (b *Broadcaster) Notify(ctx context.Context, c Change) {
	b.local.Invalidate(c)

	// The admin request may end the moment its response is written; the
	// publish should still go out.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if err := b.rdb.Publish(ctx, Channel, c.String()).Err(); err != nil {
		b.logger.WarnContext(ctx, "publishing policy change failed; other instances will see it when their cache expires",
			slog.String("change", c.String()),
			slog.Any("error", err))
	}
}

// NotifyClient announces a change to one client, such as a revoked API key,
// as a client change. It satisfies client.Notifier.
func (b *Broadcaster) NotifyClient(ctx context.Context, clientID string) {
	b.Notify(ctx, ForClient(clientID))
}

// Subscriber applies changes published by any instance to the local cache.
type Subscriber struct {
	rdb    *redis.Client
	target Invalidator
	logger *slog.Logger

	healthEvery time.Duration
	minBackoff  time.Duration
	maxBackoff  time.Duration
}

// SubscriberOption configures a Subscriber.
type SubscriberOption func(*Subscriber)

// WithHealthCheck sets how long the subscription may stay silent before it
// is pinged, and how long the ping may go unanswered before the connection
// is presumed dead. The default is 30 seconds.
func WithHealthCheck(every time.Duration) SubscriberOption {
	return func(s *Subscriber) { s.healthEvery = every }
}

// WithBackoff sets the delays between attempts to resubscribe after a
// failure: starting at minimum and doubling up to maximum. The defaults are
// 100ms and 10s.
func WithBackoff(minimum, maximum time.Duration) SubscriberOption {
	return func(s *Subscriber) { s.minBackoff, s.maxBackoff = minimum, maximum }
}

// NewSubscriber returns a Subscriber that applies changes from rdb to
// target.
func NewSubscriber(rdb *redis.Client, target Invalidator, logger *slog.Logger, opts ...SubscriberOption) *Subscriber {
	s := &Subscriber{
		rdb:         rdb,
		target:      target,
		logger:      logger,
		healthEvery: 30 * time.Second,
		minBackoff:  100 * time.Millisecond,
		maxBackoff:  10 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run holds the subscription until ctx ends, resubscribing after any
// failure. Messages published while it is not subscribed are lost, so the
// cache is flushed whenever the subscription is (re)established and
// whenever it fails (ADR 0005). Run returns only when ctx is done.
func (s *Subscriber) Run(ctx context.Context) {
	backoff := s.minBackoff
	for {
		subscribed, err := s.session(ctx)
		if ctx.Err() != nil {
			return
		}
		s.target.Flush()
		if subscribed {
			backoff = s.minBackoff
		}
		s.logger.WarnContext(ctx, "policy change subscription lost; cache flushed, resubscribing",
			slog.String("channel", Channel),
			slog.Duration("retry_in", backoff),
			slog.Any("error", err))

		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, s.maxBackoff)
	}
}

// session holds one subscription until it fails or ctx ends. subscribed
// reports whether it got as far as a confirmed subscription.
func (s *Subscriber) session(ctx context.Context) (subscribed bool, err error) {
	ps := s.rdb.Subscribe(ctx, Channel)
	// Receive does not watch ctx while it waits for a message; closing the
	// subscription is what unblocks it at shutdown.
	stop := context.AfterFunc(ctx, func() { _ = ps.Close() })
	defer func() {
		stop()
		_ = ps.Close() // A second Close after the AfterFunc's only reports ErrClosed.
	}()

	awaitingPong := false
	for {
		msg, err := ps.ReceiveTimeout(ctx, s.healthEvery)
		if err != nil {
			if isTimeout(err) && !awaitingPong {
				// Silence may be a dead connection that TCP has not noticed.
				// A ping must be answered within the next interval.
				if err := ps.Ping(ctx); err != nil {
					return subscribed, err
				}
				awaitingPong = true
				continue
			}
			return subscribed, err
		}
		awaitingPong = false

		switch m := msg.(type) {
		case *redis.Subscription:
			// This arrives on the first subscribe and again whenever
			// go-redis reconnects by itself. Either way, anything published
			// before it was missed.
			subscribed = true
			s.target.Flush()
		case *redis.Message:
			c, err := ParseChange(m.Payload)
			if err != nil {
				// Something unexpected is publishing on the channel. Dropping
				// everything is always safe.
				s.logger.WarnContext(ctx, "ignoring malformed policy change; cache flushed",
					slog.String("channel", Channel), slog.Any("error", err))
				s.target.Flush()
				continue
			}
			s.target.Invalidate(c)
		case *redis.Pong:
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
