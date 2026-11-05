package policy_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/cache"
	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/policy"
)

// fakeClock is a settable clock for cache expiry.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func mustResolve(t *testing.T, r *policy.Resolver, id domain.Identity, method, path string) domain.ResolvedLimit {
	t.Helper()
	got, err := r.Resolve(t.Context(), id, method, path)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return got
}

func expectReads(t *testing.T, s *fakeStore, clients, policies, defaults int) {
	t.Helper()
	c, p, d := s.reads()
	if c != clients || p != policies || d != defaults {
		t.Errorf("reads: clients %d, policies %d, defaults %d; want %d, %d, %d", c, p, d, clients, policies, defaults)
	}
}

func TestResolverCachesUntilTheTTL(t *testing.T) {
	s := plans()
	clock := newFakeClock()
	r := policy.NewResolver(s, s, policy.WithTTL(10*time.Second), policy.WithClock(clock.Now))

	for range 5 {
		mustResolve(t, r, keyed(customerApp), http.MethodPost, "/api/login")
		mustResolve(t, r, anonymous, http.MethodGet, "/")
	}
	expectReads(t, s, 1, 1, 1)

	clock.Advance(10*time.Second - time.Nanosecond)
	mustResolve(t, r, keyed(customerApp), http.MethodGet, "/")
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 1, 1, 1)

	// At the TTL every entry has expired, and a change made meanwhile is
	// seen even though nobody announced it.
	s.set(func(s *fakeStore) { s.clients[customerApp] = premiumID })
	clock.Advance(time.Nanosecond)
	if got := mustResolve(t, r, keyed(customerApp), http.MethodGet, "/"); got.PolicyName != "Premium" {
		t.Errorf("after the TTL resolved %s, want Premium", got.PolicyName)
	}
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 2, 3, 2)
}

func TestResolverInvalidatesOnlyWhatChanged(t *testing.T) {
	s := plans()
	r := policy.NewResolver(s, s, policy.WithTTL(time.Hour))
	mustResolve(t, r, keyed(customerApp), http.MethodGet, "/")
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 1, 1, 1)

	// A client change reloads that client's assignment only.
	s.set(func(s *fakeStore) { s.clients[customerApp] = premiumID })
	r.Invalidate(policy.ForClient(customerApp))
	if got := mustResolve(t, r, keyed(customerApp), http.MethodGet, "/"); got.PolicyName != "Premium" {
		t.Errorf("after a client change resolved %s, want Premium", got.PolicyName)
	}
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 2, 2, 1)

	// A policy change reloads that policy and the default, since any
	// policy write may move the default flag.
	s.set(func(s *fakeStore) {
		s.rules[premiumID] = append(s.rules[premiumID], rule("root", "*", "/", 1))
	})
	r.Invalidate(policy.ForPolicy(premiumID))
	if got := mustResolve(t, r, keyed(customerApp), http.MethodGet, "/"); got.RuleID != "root" {
		t.Errorf("after a policy change matched rule %q, want the new rule", got.RuleID)
	}
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 2, 3, 2)

	// Flush reloads everything.
	r.Flush()
	mustResolve(t, r, keyed(customerApp), http.MethodGet, "/")
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 3, 5, 3)
}

func TestResolverDefaultMovesOnAnyPolicyChange(t *testing.T) {
	s := plans()
	r := policy.NewResolver(s, s, policy.WithTTL(time.Hour))
	mustResolve(t, r, anonymous, http.MethodGet, "/")

	// The flag moves to Enterprise. The write that moved it touched both
	// policies, but one change notice for either must be enough.
	s.set(func(s *fakeStore) { s.defaultID = enterpriseID })
	r.Invalidate(policy.ForPolicy(enterpriseID))
	if got := mustResolve(t, r, anonymous, http.MethodGet, "/"); got.PolicyName != "Enterprise" {
		t.Errorf("after the default moved resolved %s, want Enterprise", got.PolicyName)
	}
}

func TestResolverCollapsesConcurrentMisses(t *testing.T) {
	s := plans()
	hold := make(chan struct{})
	s.set(func(s *fakeStore) { s.hold = hold })
	r := policy.NewResolver(s, s)

	const callers = 64
	var (
		wg   sync.WaitGroup
		errs atomic.Int64
	)
	for range callers {
		wg.Go(func() {
			got, err := r.Resolve(t.Context(), keyed(merchantApp), http.MethodGet, "/api/orders/1")
			if err != nil || got.RuleID != "orders" {
				errs.Add(1)
			}
		})
	}
	// Let the first load start and the other callers pile up behind it.
	waitUntil(t, func() bool { _, p, _ := s.reads(); return p == 1 })
	time.Sleep(20 * time.Millisecond)
	close(hold)
	wg.Wait()

	if n := errs.Load(); n != 0 {
		t.Errorf("%d of %d callers failed", n, callers)
	}
	// Whether a caller joined the flight or arrived after it stored its
	// result, nobody may have started a second load.
	expectReads(t, s, 1, 1, 0)
}

func TestResolverInvalidationBeatsALoadInFlight(t *testing.T) {
	s := plans()
	hold := make(chan struct{})
	s.set(func(s *fakeStore) { s.hold = hold })
	r := policy.NewResolver(s, s, policy.WithTTL(time.Hour))

	// A load reads Premium's old rules and stalls before returning.
	first := make(chan domain.ResolvedLimit, 1)
	go func() {
		got, _ := r.Resolve(context.WithoutCancel(t.Context()), keyed(merchantApp), http.MethodGet, "/api/orders/1")
		first <- got
	}()
	waitUntil(t, func() bool { _, p, _ := s.reads(); return p == 1 })

	// Meanwhile the policy changes and the change is announced.
	s.set(func(s *fakeStore) {
		s.rules[premiumID] = nil
		s.hold = nil
	})
	r.Invalidate(policy.ForPolicy(premiumID))

	// A request arriving after the change starts its own load instead of
	// joining the stalled one, so it sees the change without waiting.
	fresh := make(chan domain.ResolvedLimit, 1)
	go func() {
		got, _ := r.Resolve(context.WithoutCancel(t.Context()), keyed(merchantApp), http.MethodGet, "/api/orders/1")
		fresh <- got
	}()
	select {
	case got := <-fresh:
		if got.RuleID != "" {
			t.Errorf("a request after the change matched rule %q, want the policy limit", got.RuleID)
		}
	case <-time.After(2 * time.Second):
		t.Error("a request after the change joined the stalled load instead of starting a fresh one")
	}
	close(hold)

	// The stalled request may see the value it read, but that value must
	// not be cached: the next request sees the change.
	<-first
	if got := mustResolve(t, r, keyed(merchantApp), http.MethodGet, "/api/orders/1"); got.RuleID != "" {
		t.Errorf("after the change matched rule %q, want the policy limit: a stale load was cached", got.RuleID)
	}
}

func TestResolverDoesNotCacheErrors(t *testing.T) {
	s := plans()
	failure := errors.New("database is down")
	s.set(func(s *fakeStore) { s.err = failure })
	r := policy.NewResolver(s, s)

	if _, err := r.Resolve(t.Context(), anonymous, http.MethodGet, "/"); !errors.Is(err, failure) {
		t.Fatalf("Resolve error = %v, want the source's failure", err)
	}
	s.set(func(s *fakeStore) { s.err = nil })
	if got := mustResolve(t, r, anonymous, http.MethodGet, "/"); got.PolicyName != "Free" {
		t.Errorf("after recovery resolved %s, want Free", got.PolicyName)
	}
}

func TestResolverCallerGivingUpDoesNotFailOthers(t *testing.T) {
	s := plans()
	hold := make(chan struct{})
	s.set(func(s *fakeStore) { s.hold = hold })
	r := policy.NewResolver(s, s)

	impatient, cancel := context.WithCancel(t.Context())
	impatientErr := make(chan error, 1)
	go func() {
		_, err := r.Resolve(impatient, anonymous, http.MethodGet, "/")
		impatientErr <- err
	}()
	waitUntil(t, func() bool { _, p, _ := s.reads(); return p == 1 })

	patient := make(chan error, 1)
	go func() {
		_, err := r.Resolve(t.Context(), anonymous, http.MethodGet, "/")
		patient <- err
	}()

	cancel()
	if err := <-impatientErr; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled caller got %v, want context.Canceled", err)
	}
	close(hold)
	if err := <-patient; err != nil {
		t.Errorf("the other caller failed with %v; the shared load must outlive the caller that started it", err)
	}
	// The load finished and was cached despite its starter giving up.
	mustResolve(t, r, anonymous, http.MethodGet, "/")
	expectReads(t, s, 0, 1, 1)
}

func TestChangeWireForm(t *testing.T) {
	const id = "01a1a2fb-68ca-772e-a1e6-072972830b19"
	for _, c := range []policy.Change{policy.ForClient(id), policy.ForPolicy(id)} {
		got, err := policy.ParseChange(c.String())
		if err != nil || got != c {
			t.Errorf("ParseChange(%q) = %+v, %v; want %+v", c.String(), got, err, c)
		}
	}
	if s := policy.ForClient(id).String(); s != "client:"+id {
		t.Errorf("wire form = %q", s)
	}
	for _, bad := range []string{"", "client", "client:", "client:nope", "key:" + id, ":" + id, "policy:" + id + ":x"} {
		if c, err := policy.ParseChange(bad); err == nil {
			t.Errorf("ParseChange(%q) = %+v, want an error", bad, c)
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a condition")
		}
		time.Sleep(time.Millisecond)
	}
}

// lookups counts cache lookups by cache name.
type lookups struct {
	mu     sync.Mutex
	counts map[string][2]int // [misses, hits]
}

func (l *lookups) observer(name string) cache.Observer {
	return lookupFunc(func(r cache.Result) {
		l.mu.Lock()
		defer l.mu.Unlock()
		c := l.counts[name]
		if r == cache.Hit {
			c[1]++
		} else {
			c[0]++
		}
		l.counts[name] = c
	})
}

type lookupFunc func(r cache.Result)

func (f lookupFunc) Lookup(r cache.Result) { f(r) }

func TestResolverReportsEachCachesLookups(t *testing.T) {
	s := plans()
	l := &lookups{counts: map[string][2]int{}}
	r := policy.NewResolver(s, s, policy.WithTTL(time.Hour), policy.WithCacheObserver(l.observer))

	for range 3 {
		mustResolve(t, r, keyed(merchantApp), http.MethodGet, "/")
		mustResolve(t, r, anonymous, http.MethodGet, "/")
	}

	// A keyed caller looks up its client's policy ID, then that policy
	// (Premium); an anonymous one the default ID, then the default policy
	// (Free).
	want := map[string][2]int{
		policy.CacheClientPolicy:  {1, 2},
		policy.CacheDefaultPolicy: {1, 2},
		policy.CachePolicy:        {2, 4},
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for name, w := range want {
		if got := l.counts[name]; got != w {
			t.Errorf("%s: [misses hits] = %v, want %v", name, got, w)
		}
	}
	if len(l.counts) != len(want) {
		t.Errorf("caches observed: %v, want exactly %v", l.counts, want)
	}
}
