//go:build integration

package integration

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Rimaz17/RateGuard/internal/policy"
)

// observed wraps an Invalidator and records what reaches it, so tests can
// tell when a subscription is live and what it has applied.
type observed struct {
	inner   policy.Invalidator
	flushes atomic.Int64

	mu          sync.Mutex
	invalidated []policy.Change
}

func (o *observed) Invalidate(c policy.Change) {
	o.mu.Lock()
	o.invalidated = append(o.invalidated, c)
	o.mu.Unlock()
	o.inner.Invalidate(c)
}

func (o *observed) Flush() {
	o.flushes.Add(1)
	o.inner.Flush()
}

func (o *observed) changes() []policy.Change {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]policy.Change(nil), o.invalidated...)
}

// noopInvalidator counts nothing and drops nothing.
type noopInvalidator struct{}

func (noopInvalidator) Invalidate(policy.Change) {}
func (noopInvalidator) Flush()                   {}

func newRedisClient(t *testing.T, url string) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parsing redis URL: %v", err)
	}
	opts.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// runSubscriber starts a subscriber for target and waits until its
// subscription is confirmed, which it signals by flushing. It is stopped
// when the test ends.
func runSubscriber(t *testing.T, rdb *redis.Client, target *observed, opts ...policy.SubscriberOption) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	before := target.flushes.Load()
	go func() {
		defer close(done)
		policy.NewSubscriber(rdb, target, slog.New(slog.DiscardHandler), opts...).Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("subscriber did not stop after its context was cancelled")
		}
	})
	waitFor(t, "the subscription to be confirmed", func() bool { return target.flushes.Load() > before })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

const someID = "01a1a2fb-68ca-772e-a1e6-072972830b19"

func TestPublishedChangesReachSubscribers(t *testing.T) {
	_, url := startRedis(t.Context(), t)
	publisher := newRedisClient(t, url)

	var targets [3]*observed
	for i := range targets {
		targets[i] = &observed{inner: noopInvalidator{}}
		runSubscriber(t, newRedisClient(t, url), targets[i])
	}

	local := &observed{inner: noopInvalidator{}}
	b := policy.NewBroadcaster(publisher, local, slog.New(slog.DiscardHandler))
	b.Notify(t.Context(), policy.ForClient(someID))
	b.Notify(t.Context(), policy.ForPolicy(someID))

	want := []policy.Change{policy.ForClient(someID), policy.ForPolicy(someID)}
	if got := local.changes(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the publishing instance applied %v directly, want %v", got, want)
	}
	for i, target := range targets {
		waitFor(t, "both changes to reach every subscriber", func() bool { return len(target.changes()) == 2 })
		if got := target.changes(); got[0] != want[0] || got[1] != want[1] {
			t.Errorf("subscriber %d received %v, want %v in order", i, got, want)
		}
	}
}

func TestSubscriberFlushesOnMalformedMessages(t *testing.T) {
	_, url := startRedis(t.Context(), t)
	rdb := newRedisClient(t, url)
	target := &observed{inner: noopInvalidator{}}
	runSubscriber(t, newRedisClient(t, url), target)

	before := target.flushes.Load()
	if err := rdb.Publish(t.Context(), policy.Channel, "nonsense").Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a flush for the malformed message", func() bool { return target.flushes.Load() > before })

	// The subscription carries on.
	if err := rdb.Publish(t.Context(), policy.Channel, policy.ForClient(someID).String()).Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next valid change", func() bool { return len(target.changes()) == 1 })
}

// TestSubscriberResubscribesAfterTheConnectionIsKilled covers ADR 0005's
// warning: messages published while disconnected are lost, so the cache
// must be flushed when the subscription comes back.
func TestSubscriberResubscribesAfterTheConnectionIsKilled(t *testing.T) {
	_, url := startRedis(t.Context(), t)
	admin := newRedisClient(t, url)
	target := &observed{inner: noopInvalidator{}}
	runSubscriber(t, newRedisClient(t, url), target, policy.WithBackoff(10*time.Millisecond, 100*time.Millisecond))

	before := target.flushes.Load()
	killed, err := admin.ClientKillByFilter(t.Context(), "TYPE", "pubsub").Result()
	if err != nil || killed != 1 {
		t.Fatalf("CLIENT KILL TYPE pubsub = %d, %v; want one connection killed", killed, err)
	}
	waitFor(t, "a flush after the connection dropped", func() bool { return target.flushes.Load() > before })
	waitFor(t, "the subscription to come back", func() bool {
		n, err := admin.PubSubNumSub(t.Context(), policy.Channel).Result()
		return err == nil && n[policy.Channel] == 1
	})

	if err := admin.Publish(t.Context(), policy.Channel, policy.ForPolicy(someID).String()).Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a change after resubscribing", func() bool { return len(target.changes()) == 1 })
}

// TestSubscriberDetectsASilentConnection pauses Redis so the subscriber's
// health-check ping goes unanswered, as it would on a connection that died
// without a reset. The subscriber must give up on it, flush, and come back.
func TestSubscriberDetectsASilentConnection(t *testing.T) {
	_, url := startRedis(t.Context(), t)
	admin := newRedisClient(t, url)
	target := &observed{inner: noopInvalidator{}}
	runSubscriber(t, newRedisClient(t, url), target,
		policy.WithHealthCheck(150*time.Millisecond),
		policy.WithBackoff(10*time.Millisecond, 100*time.Millisecond))

	before := target.flushes.Load()
	if err := admin.ClientPause(t.Context(), 1500*time.Millisecond).Err(); err != nil {
		t.Fatalf("CLIENT PAUSE: %v", err)
	}
	waitFor(t, "a flush after the unanswered ping", func() bool { return target.flushes.Load() > before })

	waitFor(t, "the subscription to come back after the pause", func() bool {
		n, err := admin.PubSubNumSub(t.Context(), policy.Channel).Result()
		return err == nil && n[policy.Channel] == 1
	})
	if err := admin.Publish(t.Context(), policy.Channel, policy.ForClient(someID).String()).Err(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a change after the pause", func() bool { return len(target.changes()) == 1 })
}

func TestBroadcasterInvalidatesLocallyWhenRedisIsDown(t *testing.T) {
	// Nothing listens on port 1, so every publish fails at once.
	rdb := newRedisClient(t, "redis://127.0.0.1:1/0")
	local := &observed{inner: noopInvalidator{}}
	b := policy.NewBroadcaster(rdb, local, slog.New(slog.DiscardHandler))

	start := time.Now()
	b.Notify(t.Context(), policy.ForClient(someID))
	if got := local.changes(); len(got) != 1 || got[0] != policy.ForClient(someID) {
		t.Errorf("local changes = %v, want the change applied despite the failed publish", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Notify took %v with Redis down; the publish timeout should bound it", elapsed)
	}
}
