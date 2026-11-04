package limiter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// The primary is stubbed here with a store that can be made to fail. The
// breaker around the real Redis store, with Redis actually stopped, is
// covered in test/integration.

// flaky is a primary store that answers from an in-memory store until it is
// told to fail, or to hang until its caller's context ends.
type flaky struct {
	inner limiter.Store

	mu    sync.Mutex
	err   error
	hang  bool
	calls int
}

func newFlaky() *flaky { return &flaky{inner: limiter.NewMemory(nil)} }

func (f *flaky) Take(ctx context.Context, key string, l limiter.Limit) (limiter.Decision, error) {
	if err := f.before(ctx); err != nil {
		return limiter.Decision{}, err
	}
	return f.inner.Take(ctx, key, l)
}

func (f *flaky) Peek(ctx context.Context, key string, l limiter.Limit) (limiter.Decision, error) {
	if err := f.before(ctx); err != nil {
		return limiter.Decision{}, err
	}
	return f.inner.Peek(ctx, key, l)
}

func (f *flaky) before(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	err, hang := f.err, f.hang
	f.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *flaky) set(err error, hang bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err, f.hang = err, hang
}

func (f *flaky) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// observed records what the breaker reported.
type observed struct {
	mu       sync.Mutex
	degraded map[limiter.FailureMode]int
	errors   int
	states   []limiter.BreakerState
}

func newObserved() *observed {
	return &observed{degraded: map[limiter.FailureMode]int{}}
}

func (o *observed) DegradedDecision(m limiter.FailureMode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.degraded[m]++
}

func (o *observed) RedisError() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errors++
}

func (o *observed) BreakerStateChanged(s limiter.BreakerState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states = append(o.states, s)
}

func (o *observed) counts() (degraded, errs int, states []limiter.BreakerState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, n := range o.degraded {
		degraded += n
	}
	return degraded, o.errors, append([]limiter.BreakerState(nil), o.states...)
}

var errRedisDown = errors.New("dial tcp 10.0.0.5:6379: connect: connection refused")

// slowly refills, so nothing refills during a test.
var slowly = limiter.Limit{Capacity: 10, RefillRate: 1.0 / 3600}

type fixture struct {
	primary  *flaky
	observed *observed
	store    *limiter.Resilient
	logs     *bytes.Buffer
	clock    *clock
}

func newFixture(t *testing.T, mode limiter.FailureMode, change ...func(*limiter.ResilientConfig)) *fixture {
	t.Helper()
	f := &fixture{primary: newFlaky(), observed: newObserved(), logs: &bytes.Buffer{}, clock: newClock()}
	cfg := limiter.ResilientConfig{
		Mode:             mode,
		InstanceCount:    3,
		Timeout:          50 * time.Millisecond,
		FailureThreshold: 3,
		Cooldown:         100 * time.Millisecond,
		Observer:         f.observed,
		Logger:           slog.New(slog.NewJSONHandler(&syncWriter{w: f.logs}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:              f.clock.Now,
	}
	for _, c := range change {
		c(&cfg)
	}
	s, err := limiter.NewResilient(f.primary, limiter.NewMemory(nil), cfg)
	if err != nil {
		t.Fatalf("NewResilient: %v", err)
	}
	f.store = s
	return f
}

// syncWriter lets the log buffer be written from several goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// logLines returns the log lines at level.
func (f *fixture) logLines(t *testing.T, level string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(f.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		if m["level"] == level {
			out = append(out, m)
		}
	}
	return out
}

func TestResilientDecidesFromThePrimaryWhileHealthy(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	for i := range 10 {
		d := mustTake(t, f.store, "k", slowly)
		if !d.Allowed || d.Remaining != 9-i {
			t.Fatalf("take %d = %+v, want allowed with %d remaining", i+1, d, 9-i)
		}
	}
	if d := mustTake(t, f.store, "k", slowly); d.Allowed {
		t.Errorf("take 11 = %+v, want the primary's refusal", d)
	}
	degraded, errs, states := f.observed.counts()
	if degraded != 0 || errs != 0 || len(states) != 1 || states[0] != limiter.BreakerClosed {
		t.Errorf("observed %d degraded, %d errors, states %v; want none, none, [closed]", degraded, errs, states)
	}
}

func TestResilientDegradesTheRequestThatSawTheFailure(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(errRedisDown, false)

	d, err := f.store.Take(t.Context(), "k", slowly)
	if err != nil {
		t.Fatalf("Take with Redis down returned an error: %v; it must degrade instead", err)
	}
	if !d.Allowed {
		t.Errorf("decision = %+v, want allowed from the local bucket", d)
	}
	degraded, errs, _ := f.observed.counts()
	if degraded != 1 || errs != 1 {
		t.Errorf("observed %d degraded and %d errors, want 1 and 1", degraded, errs)
	}
	if s := f.store.State(); s != limiter.BreakerClosed {
		t.Errorf("breaker is %v after one failure, want closed until the threshold", s)
	}
}

func TestResilientOpensAfterConsecutiveFailures(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(errRedisDown, false)

	for range 3 {
		mustTake(t, f.store, "k", slowly)
	}
	if s := f.store.State(); s != limiter.BreakerOpen {
		t.Fatalf("breaker is %v after 3 failures, want open", s)
	}
	calls := f.primary.callCount()
	for range 5 {
		mustTake(t, f.store, "k", slowly)
	}
	if n := f.primary.callCount(); n != calls {
		t.Errorf("the open breaker made %d calls to Redis, want none", n-calls)
	}
	degraded, errs, states := f.observed.counts()
	if degraded != 8 || errs != 3 {
		t.Errorf("observed %d degraded and %d errors, want 8 (every decision) and 3 (only the calls made)", degraded, errs)
	}
	if want := []limiter.BreakerState{limiter.BreakerClosed, limiter.BreakerOpen}; !equalStates(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
}

func TestResilientRecoversThroughAProbe(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(errRedisDown, false)
	for range 3 {
		mustTake(t, f.store, "k", slowly)
	}

	// A probe after the cooldown that fails reopens the breaker.
	time.Sleep(110 * time.Millisecond)
	mustTake(t, f.store, "k", slowly)
	if s := f.store.State(); s != limiter.BreakerOpen {
		t.Fatalf("after a failed probe the breaker is %v, want open again", s)
	}

	// Once Redis is back, the next probe closes it and decisions come
	// from Redis again.
	f.primary.set(nil, false)
	time.Sleep(110 * time.Millisecond)
	d := mustTake(t, f.store, "k", slowly)
	if s := f.store.State(); s != limiter.BreakerClosed {
		t.Fatalf("after a good probe the breaker is %v, want closed", s)
	}
	// Redis's bucket was never touched during the outage, so it is full.
	if !d.Allowed || d.Remaining != 9 {
		t.Errorf("first decision after recovery = %+v, want Redis's full bucket", d)
	}
	_, _, states := f.observed.counts()
	want := []limiter.BreakerState{
		limiter.BreakerClosed, limiter.BreakerOpen,
		limiter.BreakerHalfOpen, limiter.BreakerOpen,
		limiter.BreakerHalfOpen, limiter.BreakerClosed,
	}
	if !equalStates(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
}

func TestResilientLocalBucketsShareTheLimit(t *testing.T) {
	tests := []struct {
		name      string
		capacity  int
		instances int
		allowed   int
	}{
		{"divides evenly", 9, 3, 3},
		{"rounds down", 10, 3, 3},
		{"never below one", 2, 3, 1},
		{"one instance keeps the limit", 10, 1, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, limiter.FailLocal, func(c *limiter.ResilientConfig) { c.InstanceCount = tt.instances })
			f.primary.set(errRedisDown, false)
			l := limiter.Limit{Capacity: tt.capacity, RefillRate: 1.0 / 3600}

			allowed := 0
			for range tt.capacity + 5 {
				d := mustTake(t, f.store, "k", l)
				if d.Limit != tt.capacity {
					t.Fatalf("degraded decision reports limit %d, want the policy's %d", d.Limit, tt.capacity)
				}
				if d.Allowed {
					allowed++
				}
			}
			if allowed != tt.allowed {
				t.Errorf("local bucket allowed %d, want %d", allowed, tt.allowed)
			}
		})
	}
}

func TestResilientLocalBucketsRefillAtTheirShareOfTheRate(t *testing.T) {
	f := newFixture(t, limiter.FailLocal, func(c *limiter.ResilientConfig) { c.InstanceCount = 2 })
	f.primary.set(errRedisDown, false)
	// 4 a second across two instances is 2 a second here: a token each 500ms.
	l := limiter.Limit{Capacity: 4, RefillRate: 4}
	mustTake(t, f.store, "k", l)
	mustTake(t, f.store, "k", l)
	d := mustTake(t, f.store, "k", l)
	if d.Allowed {
		t.Fatalf("third take = %+v, want refused by the 2-token local bucket", d)
	}
	if d.RetryAfter <= 400*time.Millisecond || d.RetryAfter > 500*time.Millisecond {
		t.Errorf("RetryAfter = %v, want just under 500ms at half the rate", d.RetryAfter)
	}
}

func TestResilientOpenModeAllows(t *testing.T) {
	f := newFixture(t, limiter.FailOpen)
	f.primary.set(errRedisDown, false)
	for range 20 {
		d := mustTake(t, f.store, "k", slowly)
		if want := (limiter.Decision{Allowed: true, Limit: 10, Remaining: 10}); d != want {
			t.Fatalf("open-mode decision = %+v, want %+v", d, want)
		}
	}
	if degraded, _, _ := f.observed.counts(); degraded != 20 {
		t.Errorf("degraded = %d, want 20", degraded)
	}
}

func TestResilientClosedModeRefuses(t *testing.T) {
	f := newFixture(t, limiter.FailClosed)
	f.primary.set(errRedisDown, false)
	d := mustTake(t, f.store, "k", slowly)
	want := limiter.Decision{Allowed: false, Limit: 10, ResetAfter: 100 * time.Millisecond, RetryAfter: 100 * time.Millisecond}
	if d != want {
		t.Errorf("closed-mode decision = %+v, want %+v", d, want)
	}
}

func TestResilientTimesOutAHungRedis(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(nil, true)

	start := time.Now()
	d := mustTake(t, f.store, "k", slowly)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a hung Redis held the decision for %v, want about the 50ms timeout", elapsed)
	}
	if !d.Allowed {
		t.Errorf("decision = %+v, want a degraded allow", d)
	}
	if _, errs, _ := f.observed.counts(); errs != 1 {
		t.Errorf("errors = %d, want 1: a timeout is a failure", errs)
	}
}

func TestResilientIgnoresCallersThatGiveUp(t *testing.T) {
	f := newFixture(t, limiter.FailLocal, func(c *limiter.ResilientConfig) { c.Timeout = time.Hour })
	f.primary.set(nil, true)

	for range 10 {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
		_, err := f.store.Take(ctx, "k", slowly)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Take for a caller that gave up: error = %v, want its context's error", err)
		}
	}
	if s := f.store.State(); s != limiter.BreakerClosed {
		t.Errorf("breaker is %v after callers gave up, want closed: they say nothing about Redis", s)
	}
	if degraded, errs, _ := f.observed.counts(); degraded != 0 || errs != 0 {
		t.Errorf("observed %d degraded and %d errors, want none", degraded, errs)
	}
}

func TestResilientPeekDegradesWithoutTaking(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(errRedisDown, false)
	for range 3 {
		d, err := f.store.Peek(t.Context(), "k", slowly)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Allowed || d.Remaining != 3 || d.Limit != 10 {
			t.Errorf("degraded Peek = %+v, want the full local bucket of 3 out of a limit of 10", d)
		}
	}
}

func TestResilientRefusesInvalidLimits(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	if _, err := f.store.Take(t.Context(), "k", limiter.Limit{Capacity: 0, RefillRate: 1}); !errors.Is(err, limiter.ErrInvalidLimit) {
		t.Errorf("error = %v, want ErrInvalidLimit", err)
	}
	if n := f.primary.callCount(); n != 0 {
		t.Errorf("an invalid limit reached Redis %d times", n)
	}
}

func TestResilientSpacesOutDegradedWarnings(t *testing.T) {
	f := newFixture(t, limiter.FailLocal, func(c *limiter.ResilientConfig) { c.FailureThreshold = 100 })
	f.primary.set(errRedisDown, false)

	for range 50 {
		mustTake(t, f.store, "k", slowly)
	}
	warns := f.logLines(t, "WARN")
	if len(warns) != 1 || warns[0]["degraded_decisions"] != 1.0 {
		t.Fatalf("after 50 degraded decisions in an instant, warnings = %v; want one, covering the first decision", warns)
	}

	f.clock.Advance(10 * time.Second)
	mustTake(t, f.store, "k", slowly)
	warns = f.logLines(t, "WARN")
	if len(warns) != 2 || warns[1]["degraded_decisions"] != 50.0 {
		t.Fatalf("10s later, warnings = %v; want a second one covering the 49 suppressed and this one", warns)
	}
	if warns[1]["failure_mode"] != "local" || !strings.Contains(warns[1]["error"].(string), "connection refused") {
		t.Errorf("warning = %v, want the failure mode and the cause", warns[1])
	}
}

func TestResilientLogsTheBreakerOpeningAndClosing(t *testing.T) {
	f := newFixture(t, limiter.FailLocal)
	f.primary.set(errRedisDown, false)
	for range 3 {
		mustTake(t, f.store, "k", slowly)
	}
	f.primary.set(nil, false)
	time.Sleep(110 * time.Millisecond)
	mustTake(t, f.store, "k", slowly)

	var opened, closed bool
	for _, w := range f.logLines(t, "WARN") {
		opened = opened || strings.Contains(w["msg"].(string), "breaker opened")
	}
	for _, i := range f.logLines(t, "INFO") {
		if strings.Contains(i["msg"].(string), "breaker closed") {
			closed = true
			// The first of the three degraded decisions was logged; the
			// other two are reported on recovery.
			if i["degraded_decisions_since_last_warning"] != 2.0 {
				t.Errorf("recovery line = %v, want the 2 unreported degraded decisions", i)
			}
		}
	}
	if !opened || !closed {
		t.Errorf("opened logged %v, closed logged %v; want both", opened, closed)
	}
}

func TestNewResilientRejectsInvalidConfig(t *testing.T) {
	valid := limiter.ResilientConfig{Mode: limiter.FailLocal, InstanceCount: 1, Timeout: time.Second, FailureThreshold: 1, Cooldown: time.Second}
	for name, change := range map[string]func(*limiter.ResilientConfig){
		"unknown mode":    func(c *limiter.ResilientConfig) { c.Mode = "unlimited" },
		"no instances":    func(c *limiter.ResilientConfig) { c.InstanceCount = 0 },
		"zero timeout":    func(c *limiter.ResilientConfig) { c.Timeout = 0 },
		"zero cooldown":   func(c *limiter.ResilientConfig) { c.Cooldown = 0 },
		"zero threshold":  func(c *limiter.ResilientConfig) { c.FailureThreshold = 0 },
		"empty mode name": func(c *limiter.ResilientConfig) { c.Mode = "" },
	} {
		cfg := valid
		change(&cfg)
		if _, err := limiter.NewResilient(newFlaky(), limiter.NewMemory(nil), cfg); err == nil {
			t.Errorf("%s: NewResilient succeeded, want an error", name)
		}
	}
	if _, err := limiter.NewResilient(newFlaky(), limiter.NewMemory(nil), valid); err != nil {
		t.Errorf("valid config: %v", err)
	}
}

func equalStates(a, b []limiter.BreakerState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
