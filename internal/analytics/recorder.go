// Package analytics counts how many checks each caller has allowed and
// rejected, and reports it (ADR 0018). Counting must never slow a decision
// (ADR 0004), so usage moves in three steps:
//
//  1. The Recorder counts in process memory: one map update per check, no
//     I/O.
//  2. The Flusher moves those counts into Redis every second, where every
//     instance's counts add up.
//  3. The Rollup drains Redis into the usage_rollups table in Postgres
//     every few seconds.
//
// The Reporter answers analytics queries from Postgres.
package analytics

import (
	"slices"
	"sync"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// DefaultMaxSlots is how many distinct (minute, caller) counts a Recorder
// holds before it starts dropping checks. It is reached only when Redis has
// been unreachable for a long time with many clients active.
const DefaultMaxSlots = 100_000

// Observer is told what the usage pipeline had to discard. Its methods must
// be cheap: UsageDropped can run on the decision path.
type Observer interface {
	// UsageDropped is called with the number of checks whose usage was lost.
	UsageDropped(checks int64)
}

type slot struct {
	minute   int64 // Unix seconds at the start of the minute
	clientID string
}

type tally struct {
	allowed  int64
	rejected int64
}

// RecorderConfig configures a Recorder. The zero value is usable.
type RecorderConfig struct {
	// MaxSlots bounds the buffer; DefaultMaxSlots when zero.
	MaxSlots int

	// Observer is told about dropped checks. It may be nil.
	Observer Observer

	// Now stamps each check with its minute, or time.Now when nil. Usage is
	// a record of when checks happened, like a log timestamp; it never
	// touches a bucket (ADR 0002, ADR 0018).
	Now func() time.Time
}

// Recorder counts checks per caller per minute in memory. Record is the
// only thing the decision path calls, and it does no I/O. A Recorder is
// safe for concurrent use.
type Recorder struct {
	max      int
	observer Observer
	now      func() time.Time

	mu      sync.Mutex
	pending map[slot]tally
}

// NewRecorder returns an empty Recorder.
func NewRecorder(cfg RecorderConfig) *Recorder {
	r := &Recorder{
		max:      cfg.MaxSlots,
		observer: cfg.Observer,
		now:      cfg.Now,
		pending:  make(map[slot]tally),
	}
	if r.max <= 0 {
		r.max = DefaultMaxSlots
	}
	if r.observer == nil {
		r.observer = nopObserver{}
	}
	if r.now == nil {
		r.now = time.Now
	}
	return r
}

// Record counts one check by clientID, or by a caller identified only by
// IP when clientID is "". When the buffer is full and the check would need
// a new slot, it is dropped and the Observer is told.
func (r *Recorder) Record(clientID string, allowed bool) {
	s := slot{minute: minuteOf(r.now()), clientID: clientID}
	r.mu.Lock()
	t, ok := r.pending[s]
	if !ok && len(r.pending) >= r.max {
		r.mu.Unlock()
		r.observer.UsageDropped(1)
		return
	}
	if allowed {
		t.allowed++
	} else {
		t.rejected++
	}
	r.pending[s] = t
	r.mu.Unlock()
}

// take empties the buffer and returns what it held, oldest minute first.
func (r *Recorder) take() []domain.UsageCount {
	r.mu.Lock()
	pending := r.pending
	if len(pending) == 0 {
		r.mu.Unlock()
		return nil
	}
	r.pending = make(map[slot]tally, len(pending))
	r.mu.Unlock()

	counts := make([]domain.UsageCount, 0, len(pending))
	for s, t := range pending {
		counts = append(counts, domain.UsageCount{
			ClientID: s.clientID,
			Minute:   time.Unix(s.minute, 0).UTC(),
			Allowed:  t.allowed,
			Rejected: t.rejected,
		})
	}
	slices.SortFunc(counts, compareCounts)
	return counts
}

// restore puts counts that could not be flushed back into the buffer, to be
// tried again. Counts that no longer fit are dropped and reported.
func (r *Recorder) restore(counts []domain.UsageCount) {
	var dropped int64
	r.mu.Lock()
	for _, c := range counts {
		s := slot{minute: c.Minute.Unix(), clientID: c.ClientID}
		t, ok := r.pending[s]
		if !ok && len(r.pending) >= r.max {
			dropped += c.Allowed + c.Rejected
			continue
		}
		t.allowed += c.Allowed
		t.rejected += c.Rejected
		r.pending[s] = t
	}
	r.mu.Unlock()
	if dropped > 0 {
		r.observer.UsageDropped(dropped)
	}
}

// minuteOf returns the Unix time of the start of t's minute.
func minuteOf(t time.Time) int64 {
	sec := t.Unix()
	m := sec % 60
	if m < 0 {
		m += 60
	}
	return sec - m
}

// compareCounts orders counts by minute, then client.
func compareCounts(a, b domain.UsageCount) int {
	if c := a.Minute.Compare(b.Minute); c != 0 {
		return c
	}
	switch {
	case a.ClientID < b.ClientID:
		return -1
	case a.ClientID > b.ClientID:
		return 1
	default:
		return 0
	}
}

// checks returns how many checks counts hold.
func checks(counts []domain.UsageCount) int64 {
	var n int64
	for _, c := range counts {
		n += c.Allowed + c.Rejected
	}
	return n
}

type nopObserver struct{}

func (nopObserver) UsageDropped(int64) {}
