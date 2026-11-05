package cache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/cache"
)

var (
	// errGone stands for the source's "not found" answer.
	errGone = errors.New("gone")

	// errDown stands for the source being unreachable.
	errDown = errors.New("connection refused")
)

const (
	staleTTL    = 10 * time.Second
	staleMaxAge = time.Minute
)

// results records what an Observer is told.
type results struct {
	mu  sync.Mutex
	got []cache.Result
}

func (r *results) Lookup(res cache.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, res)
}

func (r *results) last() cache.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[len(r.got)-1]
}

func (s *source) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *source) remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.value, key)
}

// loadOrGone is load, except that a key the source lacks is the errGone
// answer rather than an empty value.
func (s *source) loadOrGone(ctx context.Context, key string) (string, error) {
	v, err := s.load(ctx, key)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.value[key]; !ok {
		return "", errGone
	}
	return v, nil
}

// staleCache returns a cache with WithStale over src, warmed with key "a",
// whose source then goes down and whose entry has just expired.
func staleCache(t *testing.T) (*cache.Cache[string], *source, *clock, *results) {
	t.Helper()
	src := newSource("a", "1")
	clk := newClock()
	obs := &results{}
	c := cache.New(staleTTL, time.Second, clk.Now, src.loadOrGone,
		cache.WithStale(staleMaxAge, errGone), cache.WithObserver(obs))
	mustGet(t, c, "a")
	src.fail(errDown)
	clk.Advance(staleTTL)
	return c, src, clk, obs
}

// eventually fails the test unless cond holds within a second.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStaleServesTheLastValueWhileTheSourceIsDown(t *testing.T) {
	c, src, _, obs := staleCache(t)

	if got := mustGet(t, c, "a"); got != "1" {
		t.Fatalf("Get after a failed reload = %q, want the last value 1", got)
	}
	if r := obs.last(); r != cache.Stale {
		t.Errorf("result = %v, want stale", r)
	}

	// Until the retry is due, lookups answer at once without touching the
	// source, so an unreachable database adds no latency to them.
	for range 5 {
		if got := mustGet(t, c, "a"); got != "1" {
			t.Fatalf("Get = %q, want 1", got)
		}
		if r := obs.last(); r != cache.Stale {
			t.Errorf("result = %v, want stale", r)
		}
	}
	if n := src.reads.Load(); n != 2 {
		t.Errorf("reads = %d, want 2: the warm-up and the one failed reload", n)
	}
}

func TestStaleRetriesInTheBackgroundOncePerInterval(t *testing.T) {
	c, src, clk, _ := staleCache(t)
	mustGet(t, c, "a") // the failed reload: read 2

	clk.Advance(time.Second)
	for range 10 {
		if got := mustGet(t, c, "a"); got != "1" {
			t.Fatalf("Get while retrying = %q, want 1", got)
		}
	}
	eventually(t, func() bool { return src.reads.Load() == 3 })
	time.Sleep(10 * time.Millisecond)
	if n := src.reads.Load(); n != 3 {
		t.Errorf("reads = %d, want 3: one retry per interval, however many lookups", n)
	}
}

func TestStaleRecoversWhenTheSourceReturns(t *testing.T) {
	c, src, clk, obs := staleCache(t)
	mustGet(t, c, "a")

	src.set("a", "2")
	src.fail(nil)
	clk.Advance(time.Second)
	mustGet(t, c, "a") // still stale; starts the retry
	eventually(t, func() bool { return mustGet(t, c, "a") == "2" })
	if r := obs.last(); r != cache.Hit {
		t.Errorf("result after recovery = %v, want hit", r)
	}
}

func TestStaleIsNotServedPastTheMaxAge(t *testing.T) {
	c, _, clk, _ := staleCache(t)
	mustGet(t, c, "a")

	clk.Advance(staleMaxAge)
	if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errDown) {
		t.Errorf("Get past the max age: error = %v, want the source's error", err)
	}
}

// A "not found" from the source is an answer, not an outage: the entry is
// dropped and never comes back as a fallback.
func TestStaleIsNotServedAfterTheSourceDisownsTheValue(t *testing.T) {
	c, src, _, _ := staleCache(t)
	src.fail(nil)
	src.remove("a")

	if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errGone) {
		t.Fatalf("Get of a removed key: error = %v, want errGone", err)
	}
	src.fail(errDown)
	if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errDown) {
		t.Errorf("Get after the removal with the source down: error = %v, want errDown, not the old value", err)
	}
}

func TestStaleIsNotServedAfterAnInvalidation(t *testing.T) {
	for name, drop := range map[string]func(*cache.Cache[string]){
		"Invalidate":     func(c *cache.Cache[string]) { c.Invalidate("a") },
		"InvalidateFunc": func(c *cache.Cache[string]) { c.InvalidateFunc(func(k, _ string) bool { return k == "a" }) },
		"Flush":          func(c *cache.Cache[string]) { c.Flush() },
	} {
		t.Run(name, func(t *testing.T) {
			for _, failedFirst := range []bool{false, true} {
				c, _, _, _ := staleCache(t)
				if failedFirst {
					mustGet(t, c, "a") // already being served stale
				}
				drop(c)
				if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errDown) {
					t.Errorf("failedFirst=%v: Get after %s = %v, want errDown, not a value from before the change", failedFirst, name, err)
				}
			}
		})
	}
}

func TestStaleIsOffByDefault(t *testing.T) {
	src := newSource("a", "1")
	clk := newClock()
	c := cache.New(staleTTL, time.Second, clk.Now, src.load)
	mustGet(t, c, "a")
	src.fail(errDown)
	clk.Advance(staleTTL)

	if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errDown) {
		t.Errorf("Get without WithStale = %v, want the source's error", err)
	}
}

func TestResultStrings(t *testing.T) {
	for r, want := range map[cache.Result]string{cache.Hit: "hit", cache.Miss: "miss", cache.Stale: "stale"} {
		if got := r.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", r, got, want)
		}
	}
}

// An entry that reaches its max age while a slow reload is running is not
// served when that reload fails.
func TestStaleIsNotServedWhenTheMaxAgePassesDuringTheReload(t *testing.T) {
	src := newSource("a", "1")
	clk := newClock()
	var slow bool
	var mu sync.Mutex
	load := func(ctx context.Context, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if slow {
			clk.Advance(time.Second)
			return "", errDown
		}
		return src.load(ctx, key)
	}
	c := cache.New(staleTTL, 5*time.Second, clk.Now, load, cache.WithStale(staleMaxAge, errGone))
	mustGet(t, c, "a")

	mu.Lock()
	slow = true
	mu.Unlock()
	clk.Advance(staleTTL + staleMaxAge - time.Millisecond)
	if _, err := c.Get(t.Context(), "a"); !errors.Is(err, errDown) {
		t.Errorf("Get whose reload outlived the max age = %v, want errDown", err)
	}
}

// hangingSource answers its first load at once and holds every later one
// until released, like a database whose connections now hang.
type hangingSource struct {
	mu      sync.Mutex
	loads   int
	value   string
	release chan struct{}
}

func (h *hangingSource) load(ctx context.Context, _ string) (string, error) {
	h.mu.Lock()
	h.loads++
	first, v := h.loads == 1, h.value
	h.mu.Unlock()
	if first {
		return v, nil
	}
	select {
	case <-h.release:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// A reload that hangs must not hold the caller for the whole load timeout
// while there is a value to fall back on: a check walks several caches, and
// their timeouts together would outlast the server's write timeout. The
// caller gets the previous value promptly, and the reload still lands.
func TestStaleAnswersAHangingReloadWithoutWaitingForIt(t *testing.T) {
	h := &hangingSource{value: "1", release: make(chan struct{})}
	clk := newClock()
	obs := &results{}
	c := cache.New(staleTTL, 5*time.Second, clk.Now, h.load,
		cache.WithStale(staleMaxAge, errGone), cache.WithObserver(obs))
	mustGet(t, c, "a")
	clk.Advance(staleTTL)

	start := time.Now()
	if got := mustGet(t, c, "a"); got != "1" {
		t.Fatalf("Get during a hanging reload = %q, want the previous value 1", got)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("Get waited %v for a hanging reload, want well under the 5s load timeout", waited)
	}
	if r := obs.last(); r != cache.Stale {
		t.Errorf("result = %v, want stale", r)
	}

	h.mu.Lock()
	h.value = "2"
	h.mu.Unlock()
	close(h.release)
	eventually(t, func() bool { return mustGet(t, c, "a") == "2" })
}
