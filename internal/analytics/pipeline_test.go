package analytics_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/analytics"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

const (
	clientA = "0193f2a0-0000-7000-8000-00000000000a"
	clientB = "0193f2a0-0000-7000-8000-00000000000b"
	clientC = "0193f2a0-0000-7000-8000-00000000000c"
)

var discard = slog.New(slog.DiscardHandler)

// clock is a settable time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// pending is an in-memory Sink and Pending. fail makes the next calls fail;
// beforeAdd runs inside Add before anything is stored.
type pending struct {
	mu        sync.Mutex
	stored    []domain.UsageCount
	failAdd   int
	failDrain error
	beforeAdd func()
}

func (p *pending) Add(_ context.Context, counts []domain.UsageCount) ([]domain.UsageCount, error) {
	p.mu.Lock()
	hook := p.beforeAdd
	p.beforeAdd = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failAdd > 0 {
		p.failAdd--
		return counts, errors.New("redis is down")
	}
	p.stored = append(p.stored, counts...)
	return nil, nil
}

func (p *pending) Drain(_ context.Context, _ int) ([]domain.UsageCount, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.stored
	p.stored = nil
	return out, p.failDrain
}

func (p *pending) total() domain.UsageTotals {
	p.mu.Lock()
	defer p.mu.Unlock()
	return totals(p.stored)
}

func totals(counts []domain.UsageCount) domain.UsageTotals {
	var t domain.UsageTotals
	for _, c := range counts {
		t.Allowed += c.Allowed
		t.Rejected += c.Rejected
	}
	return t
}

type dropCounter struct{ n atomic.Int64 }

func (d *dropCounter) UsageDropped(n int64) { d.n.Add(n) }

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRecorderCountsByClientAndMinute(t *testing.T) {
	clk := &clock{now: at("2026-11-05T10:00:59Z")}
	rec := analytics.NewRecorder(analytics.RecorderConfig{Now: clk.Now})
	sink := &pending{}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	rec.Record(clientA, true)
	rec.Record(clientA, false)
	rec.Record("", true) // a caller known only by IP
	clk.Set(at("2026-11-05T10:01:00Z"))
	rec.Record(clientA, true)
	rec.Record(clientB, false)
	f.Flush(t.Context())

	want := []domain.UsageCount{
		{ClientID: "", Minute: at("2026-11-05T10:00:00Z"), Allowed: 1},
		{ClientID: clientA, Minute: at("2026-11-05T10:00:00Z"), Allowed: 1, Rejected: 1},
		{ClientID: clientA, Minute: at("2026-11-05T10:01:00Z"), Allowed: 1},
		{ClientID: clientB, Minute: at("2026-11-05T10:01:00Z"), Rejected: 1},
	}
	if !slices.EqualFunc(sink.stored, want, sameCount) {
		t.Fatalf("flushed %+v\nwant    %+v", sink.stored, want)
	}

	// A flush empties the buffer.
	f.Flush(t.Context())
	if len(sink.stored) != len(want) {
		t.Fatalf("second flush added %d counts, want none", len(sink.stored)-len(want))
	}
}

func sameCount(a, b domain.UsageCount) bool {
	return a.ClientID == b.ClientID && a.Minute.Equal(b.Minute) && a.Allowed == b.Allowed && a.Rejected == b.Rejected
}

func TestFailedFlushIsRetried(t *testing.T) {
	rec := analytics.NewRecorder(analytics.RecorderConfig{})
	sink := &pending{failAdd: 2}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	for range 3 {
		rec.Record(clientA, true)
	}
	f.Flush(t.Context()) // fails: counts go back
	rec.Record(clientA, false)
	f.Flush(t.Context()) // fails again
	f.Flush(t.Context()) // succeeds

	if got, want := sink.total(), (domain.UsageTotals{Allowed: 3, Rejected: 1}); got != want {
		t.Fatalf("flushed %+v, want %+v: a failed flush must lose and double nothing", got, want)
	}
}

func TestRecorderDropsChecksBeyondItsBound(t *testing.T) {
	drops := &dropCounter{}
	rec := analytics.NewRecorder(analytics.RecorderConfig{MaxSlots: 2, Observer: drops})
	sink := &pending{}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	rec.Record(clientA, true)
	rec.Record(clientB, true)
	rec.Record(clientC, true) // needs a third slot: dropped
	rec.Record(clientA, false)
	f.Flush(t.Context())

	if got := drops.n.Load(); got != 1 {
		t.Errorf("dropped %d checks, want 1", got)
	}
	if got, want := sink.total(), (domain.UsageTotals{Allowed: 2, Rejected: 1}); got != want {
		t.Errorf("flushed %+v, want %+v", got, want)
	}
}

func TestCountsThatNoLongerFitAfterAFailedFlushAreDropped(t *testing.T) {
	drops := &dropCounter{}
	rec := analytics.NewRecorder(analytics.RecorderConfig{MaxSlots: 2, Observer: drops})
	// While the failing flush is under way, two new callers fill the
	// buffer, so the two counts coming back have no room.
	sink := &pending{failAdd: 1, beforeAdd: func() {
		rec.Record(clientC, true)
		rec.Record("", true)
	}}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	rec.Record(clientA, true)
	rec.Record(clientB, false)
	rec.Record(clientB, false)
	f.Flush(t.Context())
	f.Flush(t.Context())

	if got := drops.n.Load(); got != 3 {
		t.Errorf("dropped %d checks, want 3 (client A's and both of B's)", got)
	}
	if got, want := sink.total(), (domain.UsageTotals{Allowed: 2}); got != want {
		t.Errorf("flushed %+v, want %+v", got, want)
	}
}

func TestConcurrentRecordsAreAllCounted(t *testing.T) {
	rec := analytics.NewRecorder(analytics.RecorderConfig{})
	sink := &pending{}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	const workers, each = 16, 500
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				rec.Record([]string{clientA, clientB, ""}[w%3], i%4 != 0)
			}
		})
	}
	// Flushing while recording must neither lose nor double a check.
	for range 20 {
		f.Flush(t.Context())
	}
	wg.Wait()
	f.Flush(t.Context())

	got := sink.total()
	if got.Requests() != workers*each || got.Rejected != workers*each/4 {
		t.Fatalf("flushed %+v, want %d checks of which %d rejected", got, workers*each, workers*each/4)
	}
}

func TestRunFlushesOnceMoreWhenStopped(t *testing.T) {
	rec := analytics.NewRecorder(analytics.RecorderConfig{})
	sink := &pending{}
	f := analytics.NewFlusher(rec, sink, time.Hour, discard, nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	rec.Record(clientA, true)
	cancel()
	<-done

	if got := sink.total(); got.Allowed != 1 {
		t.Fatalf("after stopping, flushed %+v; want the last check flushed", got)
	}
}

func TestRunReportsChecksLostAtShutdown(t *testing.T) {
	drops := &dropCounter{}
	rec := analytics.NewRecorder(analytics.RecorderConfig{})
	f := analytics.NewFlusher(rec, &pending{failAdd: 1}, time.Hour, discard, drops)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec.Record(clientA, true)
	rec.Record(clientA, false)
	f.Run(ctx)

	if got := drops.n.Load(); got != 2 {
		t.Fatalf("reported %d checks lost, want 2", got)
	}
}

func TestRunFlushesEveryInterval(t *testing.T) {
	rec := analytics.NewRecorder(analytics.RecorderConfig{})
	sink := &pending{}
	f := analytics.NewFlusher(rec, sink, 5*time.Millisecond, discard, nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	rec.Record(clientA, true)
	deadline := time.Now().Add(5 * time.Second)
	for sink.total().Allowed != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the check was never flushed while running")
		}
		time.Sleep(time.Millisecond)
	}
}

// store is an in-memory Store.
type store struct {
	mu     sync.Mutex
	counts []domain.UsageCount
	fail   bool
}

func (s *store) AddUsage(_ context.Context, counts []domain.UsageCount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("postgres is down")
	}
	s.counts = append(s.counts, counts...)
	return nil
}

func TestRollMovesPendingCountsIntoTheStore(t *testing.T) {
	p := &pending{stored: []domain.UsageCount{{ClientID: clientA, Allowed: 3, Rejected: 1}}}
	s := &store{}
	analytics.NewRollup(p, s, time.Hour, discard, nil).Roll(t.Context())

	if got := totals(s.counts); got != (domain.UsageTotals{Allowed: 3, Rejected: 1}) {
		t.Errorf("stored %+v", got)
	}
	if got := p.total(); got.Requests() != 0 {
		t.Errorf("still pending: %+v", got)
	}
}

func TestRollPutsCountsBackWhenTheStoreFails(t *testing.T) {
	p := &pending{stored: []domain.UsageCount{{ClientID: clientA, Allowed: 3}}}
	s := &store{fail: true}
	r := analytics.NewRollup(p, s, time.Hour, discard, nil)

	r.Roll(t.Context())
	if got := p.total(); got.Allowed != 3 {
		t.Fatalf("pending after a failed store: %+v, want the 3 checks back", got)
	}

	s.fail = false
	r.Roll(t.Context())
	if got := totals(s.counts); got.Allowed != 3 {
		t.Fatalf("stored %+v after recovery, want 3 allowed", got)
	}
}

func TestRollReportsLossWhenCountsCannotGoBack(t *testing.T) {
	drops := &dropCounter{}
	p := &pending{stored: []domain.UsageCount{{ClientID: clientA, Allowed: 3, Rejected: 2}}, failAdd: 1}
	analytics.NewRollup(p, &store{fail: true}, time.Hour, discard, drops).Roll(t.Context())

	if got := drops.n.Load(); got != 5 {
		t.Fatalf("reported %d checks lost, want 5", got)
	}
}

func TestRollStoresWhatWasDrainedBeforeAnError(t *testing.T) {
	p := &pending{
		stored:    []domain.UsageCount{{ClientID: clientA, Allowed: 2}},
		failDrain: errors.New("redis went away mid-drain"),
	}
	s := &store{}
	analytics.NewRollup(p, s, time.Hour, discard, nil).Roll(t.Context())

	if got := totals(s.counts); got.Allowed != 2 {
		t.Fatalf("stored %+v, want the 2 checks drained before the error", got)
	}
}
