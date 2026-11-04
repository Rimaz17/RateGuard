package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/cache"
)

// clock is a settable clock for expiry.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// source is a loader that counts its reads and serves a settable value.
type source struct {
	mu    sync.Mutex
	value map[string]string
	reads atomic.Int32
	err   error
}

func newSource(kv ...string) *source {
	s := &source{value: map[string]string{}}
	for i := 0; i+1 < len(kv); i += 2 {
		s.value[kv[i]] = kv[i+1]
	}
	return s
}

func (s *source) load(_ context.Context, key string) (string, error) {
	s.reads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	return s.value[key], nil
}

func (s *source) set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value[key] = value
}

func mustGet(t *testing.T, c *cache.Cache[string], key string) string {
	t.Helper()
	v, err := c.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

func TestGetCachesUntilTheTTL(t *testing.T) {
	src := newSource("a", "1")
	clk := newClock()
	c := cache.New(10*time.Second, time.Second, clk.Now, src.load)

	for range 3 {
		if got := mustGet(t, c, "a"); got != "1" {
			t.Fatalf("Get = %q, want 1", got)
		}
	}
	if n := src.reads.Load(); n != 1 {
		t.Errorf("reads = %d, want 1", n)
	}

	src.set("a", "2")
	clk.Advance(10*time.Second - time.Nanosecond)
	if got := mustGet(t, c, "a"); got != "1" {
		t.Errorf("just before the TTL Get = %q, want the cached 1", got)
	}
	clk.Advance(time.Nanosecond)
	if got := mustGet(t, c, "a"); got != "2" {
		t.Errorf("at the TTL Get = %q, want the reloaded 2", got)
	}
}

func TestInvalidateDropsOneKey(t *testing.T) {
	src := newSource("a", "1", "b", "1")
	c := cache.New(time.Hour, time.Second, nil, src.load)
	mustGet(t, c, "a")
	mustGet(t, c, "b")

	src.set("a", "2")
	src.set("b", "2")
	c.Invalidate("a")
	if got := mustGet(t, c, "a"); got != "2" {
		t.Errorf("a after Invalidate = %q, want 2", got)
	}
	if got := mustGet(t, c, "b"); got != "1" {
		t.Errorf("b after invalidating a = %q, want the cached 1", got)
	}
}

func TestFlushDropsEverything(t *testing.T) {
	src := newSource("a", "1", "b", "1")
	c := cache.New(time.Hour, time.Second, nil, src.load)
	mustGet(t, c, "a")
	mustGet(t, c, "b")

	src.set("a", "2")
	src.set("b", "2")
	c.Flush()
	if a, b := mustGet(t, c, "a"), mustGet(t, c, "b"); a != "2" || b != "2" {
		t.Errorf("after Flush a, b = %q, %q; want 2, 2", a, b)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	src := newSource("a", "1")
	src.err = errors.New("database down")
	c := cache.New(time.Hour, time.Second, nil, src.load)

	if _, err := c.Get(t.Context(), "a"); err == nil {
		t.Fatal("Get with a failing loader: want an error")
	}
	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	if got := mustGet(t, c, "a"); got != "1" {
		t.Errorf("after recovery Get = %q, want 1", got)
	}
}

// gate is a loader that blocks until released, to hold a load in flight.
type gate struct {
	reads   atomic.Int32
	started chan struct{}
	release chan struct{}
	value   atomic.Value
}

func newGate(value string) *gate {
	g := &gate{started: make(chan struct{}, 16), release: make(chan struct{})}
	g.value.Store(value)
	return g
}

func (g *gate) load(ctx context.Context, _ string) (string, error) {
	g.reads.Add(1)
	g.started <- struct{}{}
	select {
	case <-g.release:
		return g.value.Load().(string), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestConcurrentMissesShareOneLoad(t *testing.T) {
	g := newGate("1")
	c := cache.New(time.Hour, 5*time.Second, nil, g.load)

	const callers = 20
	results := make(chan string, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			v, err := c.Get(t.Context(), "a")
			if err != nil {
				t.Errorf("Get: %v", err)
			}
			results <- v
		})
	}
	<-g.started
	// Give every caller time to join the flight before it finishes.
	time.Sleep(50 * time.Millisecond)
	close(g.release)
	wg.Wait()
	close(results)

	for v := range results {
		if v != "1" {
			t.Errorf("a caller got %q, want 1", v)
		}
	}
	if n := g.reads.Load(); n != 1 {
		t.Errorf("reads = %d, want 1 shared load", n)
	}
}

func TestInvalidationBeatsALoadInFlight(t *testing.T) {
	g := newGate("stale")
	c := cache.New(time.Hour, 5*time.Second, nil, g.load)

	done := make(chan string)
	go func() {
		v, _ := c.Get(t.Context(), "a")
		done <- v
	}()
	<-g.started
	// The change commits and is announced while the load is in flight.
	c.Invalidate("a")
	g.value.Store("fresh")
	close(g.release)
	<-done

	if got := mustGet(t, c, "a"); got != "fresh" {
		t.Errorf("after the in-flight load finished Get = %q, want fresh: the stale value was cached", got)
	}
}

func TestCallerGivingUpDoesNotFailOthers(t *testing.T) {
	g := newGate("1")
	c := cache.New(time.Hour, 5*time.Second, nil, g.load)

	impatient, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() {
		_, err := c.Get(impatient, "a")
		first <- err
	}()
	<-g.started

	second := make(chan string, 1)
	go func() {
		v, err := c.Get(t.Context(), "a")
		if err != nil {
			t.Errorf("patient Get: %v", err)
		}
		second <- v
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("impatient Get error = %v, want context.Canceled", err)
	}
	close(g.release)
	if v := <-second; v != "1" {
		t.Errorf("patient Get = %q, want 1", v)
	}
}
