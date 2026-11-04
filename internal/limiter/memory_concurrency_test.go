package limiter_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// These tests are meant for -race, which every test run uses here.

// TestConcurrentTakesAdmitExactlyTheCapacity freezes the clock and has many
// goroutines race for one bucket. Exactly its capacity may get through: one
// more would be a lost update, one fewer a phantom one.
func TestConcurrentTakesAdmitExactlyTheCapacity(t *testing.T) {
	m := limiter.NewMemory(newClock().Now)
	l := limiter.Limit{Capacity: 1000, RefillRate: 1}
	const goroutines, each = 64, 50

	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
		start   = make(chan struct{})
	)
	for range goroutines {
		wg.Go(func() {
			<-start
			for range each {
				d, err := m.Take(t.Context(), "hot", l)
				if err != nil {
					t.Error(err)
					return
				}
				if d.Allowed {
					allowed.Add(1)
				}
			}
		})
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != int64(l.Capacity) {
		t.Errorf("%d concurrent takes admitted %d, want exactly %d", goroutines*each, got, l.Capacity)
	}
}

// TestConcurrentKeysDoNotInterfere gives each goroutine its own key while
// all of them also hit one shared key.
func TestConcurrentKeysDoNotInterfere(t *testing.T) {
	m := limiter.NewMemory(newClock().Now)
	l := limiter.Limit{Capacity: 20, RefillRate: 1}
	const goroutines = 32

	var (
		wg     sync.WaitGroup
		shared atomic.Int64
		own    [goroutines]atomic.Int64
	)
	for g := range goroutines {
		wg.Go(func() {
			key := fmt.Sprintf("client-%d", g)
			for range 30 {
				if d, _ := m.Take(t.Context(), key, l); d.Allowed {
					own[g].Add(1)
				}
				if d, _ := m.Take(t.Context(), "shared", l); d.Allowed {
					shared.Add(1)
				}
			}
		})
	}
	wg.Wait()

	for g := range goroutines {
		if got := own[g].Load(); got != 20 {
			t.Errorf("client-%d was admitted %d times, want its own 20", g, got)
		}
	}
	if got := shared.Load(); got != 20 {
		t.Errorf("the shared key admitted %d, want 20", got)
	}
	if n := m.Len(); n != goroutines+1 {
		t.Errorf("Len = %d, want %d", n, goroutines+1)
	}
}

// TestConcurrentTakesWithAMovingClock runs takers, peekers and a clock
// that keeps advancing all at once. Admissions can never exceed what the
// bucket supplied over the time that passed.
func TestConcurrentTakesWithAMovingClock(t *testing.T) {
	c := newClock()
	m := limiter.NewMemory(c.Now)
	l := limiter.Limit{Capacity: 50, RefillRate: 1000} // a token per millisecond
	const ticks = 2000

	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
		stop    = make(chan struct{})
	)
	for range 16 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if d, _ := m.Take(t.Context(), "k", l); d.Allowed {
					allowed.Add(1)
				}
				if d, _ := m.Peek(t.Context(), "k", l); d.Remaining < 0 || d.Remaining > l.Capacity {
					t.Errorf("Peek out of bounds: %+v", d)
				}
			}
		})
	}
	for range ticks {
		c.Advance(time.Millisecond)
	}
	// Let the takers drain whatever refilled on the last tick.
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()

	supply := int64(l.Capacity) + ticks
	if got := allowed.Load(); got > supply || got < int64(l.Capacity) {
		t.Errorf("admitted %d while the bucket supplied %d", got, supply)
	}
}
