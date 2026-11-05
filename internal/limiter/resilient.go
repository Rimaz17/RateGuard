package limiter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"
)

// FailureMode says how decisions are made while Redis is unreachable
// (ADR 0001).
type FailureMode string

// Failure modes.
const (
	// FailLocal decides from an in-memory bucket per instance, sized so that
	// all instances together approximate the real limit. It is the default.
	FailLocal FailureMode = "local"

	// FailOpen allows every request.
	FailOpen FailureMode = "open"

	// FailClosed refuses every request.
	FailClosed FailureMode = "closed"
)

// BreakerState is the state of the circuit breaker around Redis.
type BreakerState int

// Breaker states, numbered as the rateguard_redis_breaker_state gauge
// reports them.
const (
	BreakerClosed BreakerState = iota
	BreakerHalfOpen
	BreakerOpen
)

// String returns the state's name.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerHalfOpen:
		return "half-open"
	case BreakerOpen:
		return "open"
	default:
		return fmt.Sprintf("BreakerState(%d)", int(s))
	}
}

// Observer is told what the circuit breaker sees, for metrics (ADR 0014).
// Its methods run on the decision path and must be cheap and non-blocking.
type Observer interface {
	// DegradedDecision is called once for every decision Redis did not make.
	DegradedDecision(mode FailureMode)

	// RedisError is called for every failed call to Redis. Calls the open
	// breaker never made are not errors.
	RedisError()

	// BreakerStateChanged is called with the breaker's new state, and once
	// with its initial state.
	BreakerStateChanged(state BreakerState)
}

// degradedWarnEvery is the most often a WARN is logged while decisions are
// degraded (ADR 0014).
const degradedWarnEvery = 10 * time.Second

// ResilientConfig configures a Resilient store.
type ResilientConfig struct {
	// Mode is what decisions do while Redis is unreachable.
	Mode FailureMode

	// InstanceCount divides each limit for the local buckets of FailLocal.
	InstanceCount int

	// Timeout bounds each call to the primary store.
	Timeout time.Duration

	// FailureThreshold is how many consecutive failures open the breaker.
	FailureThreshold uint32

	// Cooldown is how long the breaker stays open before a probe.
	Cooldown time.Duration

	// Observer receives what the breaker sees. It may be nil.
	Observer Observer

	// Logger receives the breaker's warnings. It may be nil.
	Logger *slog.Logger

	// Now is the clock that spaces out degraded warnings, or time.Now when
	// nil. It only times log lines; it never touches a bucket (ADR 0002).
	Now func() time.Time
}

// Resilient is a Store that decides from a primary store, Redis, through a
// circuit breaker, and degrades as ADR 0001 and ADR 0014 describe when the
// primary fails: to a local fallback bucket, or to allowing or refusing
// everything. It never fails a decision because Redis did; it errs only on
// an invalid limit or a caller that has gone away. A Resilient is safe for
// concurrent use.
type Resilient struct {
	primary  Store
	fallback Store
	mode     FailureMode
	divisor  int
	timeout  time.Duration
	cooldown time.Duration
	observer Observer
	logger   *slog.Logger

	breaker *gobreaker.CircuitBreaker[Decision]
	warn    warnLimiter
}

var _ Store = (*Resilient)(nil)

// errCallerGone marks a primary error caused by the caller's own context
// ending. It is neither a success nor a failure for the breaker.
var errCallerGone = errors.New("caller gave up")

// NewResilient returns a Resilient store that decides from primary and
// degrades to fallback, an in-memory store, when cfg.Mode is FailLocal.
func NewResilient(primary, fallback Store, cfg ResilientConfig) (*Resilient, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	s := &Resilient{
		primary:  primary,
		fallback: fallback,
		mode:     cfg.Mode,
		divisor:  cfg.InstanceCount,
		timeout:  cfg.Timeout,
		cooldown: cfg.Cooldown,
		observer: cfg.Observer,
		logger:   cfg.Logger,
		warn:     warnLimiter{every: degradedWarnEvery, now: cfg.Now},
	}
	if s.observer == nil {
		s.observer = nopObserver{}
	}
	if s.logger == nil {
		s.logger = slog.New(slog.DiscardHandler)
	}
	if s.warn.now == nil {
		s.warn.now = time.Now
	}
	threshold := cfg.FailureThreshold
	s.breaker = gobreaker.NewCircuitBreaker[Decision](gobreaker.Settings{
		Name:        "redis",
		MaxRequests: 1, // one probe at a time while half-open
		Timeout:     cfg.Cooldown,
		ReadyToTrip: func(c gobreaker.Counts) bool { return c.ConsecutiveFailures >= threshold },
		IsExcluded:  func(err error) bool { return errors.Is(err, errCallerGone) },
		// Called with the breaker locked: it must not call back into it.
		OnStateChange: func(_ string, from, to gobreaker.State) { s.stateChanged(from, to) },
	})
	s.observer.BreakerStateChanged(BreakerClosed)
	return s, nil
}

func (c ResilientConfig) validate() error {
	var errs []error
	switch c.Mode {
	case FailLocal, FailOpen, FailClosed:
	default:
		errs = append(errs, fmt.Errorf("unknown failure mode %q", c.Mode))
	}
	if c.InstanceCount < 1 {
		errs = append(errs, fmt.Errorf("instance count must be at least 1, got %d", c.InstanceCount))
	}
	if c.Timeout <= 0 || c.Cooldown <= 0 {
		errs = append(errs, fmt.Errorf("timeout and cooldown must be positive, got %s and %s", c.Timeout, c.Cooldown))
	}
	if c.FailureThreshold < 1 {
		errs = append(errs, errors.New("failure threshold must be at least 1"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("limiter: invalid resilient store config: %w", err)
	}
	return nil
}

// Take implements Store.
func (s *Resilient) Take(ctx context.Context, key string, l Limit) (Decision, error) {
	return s.do(ctx, key, l, false)
}

// Peek implements Store.
func (s *Resilient) Peek(ctx context.Context, key string, l Limit) (Decision, error) {
	return s.do(ctx, key, l, true)
}

// call runs a take, or a peek when peek is set, on st.
func call(ctx context.Context, st Store, key string, l Limit, peek bool) (Decision, error) {
	if peek {
		return st.Peek(ctx, key, l)
	}
	return st.Take(ctx, key, l)
}

// State returns the breaker's current state.
func (s *Resilient) State() BreakerState {
	return fromGobreaker(s.breaker.State())
}

func (s *Resilient) do(ctx context.Context, key string, l Limit, peek bool) (Decision, error) {
	if err := checkRequest(ctx, l); err != nil {
		return Decision{}, err
	}
	d, err := s.breaker.Execute(func() (Decision, error) {
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		d, err := call(callCtx, s.primary, key, l, peek)
		if err != nil && ctx.Err() != nil {
			return d, fmt.Errorf("%w: %w", errCallerGone, err)
		}
		if err != nil {
			s.observer.RedisError()
		}
		return d, err
	})
	if err == nil {
		return d, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Decision{}, fmt.Errorf("limiter: %w", ctxErr)
	}
	return s.degrade(ctx, key, l, peek, err)
}

// degrade makes the decision Redis could not (ADR 0014).
func (s *Resilient) degrade(ctx context.Context, key string, l Limit, peek bool, cause error) (Decision, error) {
	s.observer.DegradedDecision(s.mode)
	if emit, n := s.warn.tick(); emit {
		s.logger.WarnContext(ctx, "rate-limit decisions are degraded: redis is unavailable",
			slog.String("failure_mode", string(s.mode)),
			slog.Int("degraded_decisions", n),
			slog.Any("error", cause))
	}
	switch s.mode {
	case FailOpen:
		return Decision{Allowed: true, Limit: l.Capacity, Remaining: l.Capacity}, nil
	case FailClosed:
		return Decision{Allowed: false, Limit: l.Capacity, ResetAfter: s.cooldown, RetryAfter: s.cooldown}, nil
	default:
		d, err := call(ctx, s.fallback, key, localLimit(l, s.divisor), peek)
		if err != nil {
			return Decision{}, err
		}
		// Callers are told the real limit; the local bucket only
		// approximates its share of it.
		d.Limit = l.Capacity
		return d, nil
	}
}

// localLimit is one instance's share of l while Redis is unreachable:
// capacity rounded down, but never below one, and the rate divided exactly.
func localLimit(l Limit, instances int) Limit {
	return Limit{
		Capacity:   max(1, l.Capacity/instances),
		RefillRate: l.RefillRate / float64(instances),
	}
}

func (s *Resilient) stateChanged(from, to gobreaker.State) {
	s.observer.BreakerStateChanged(fromGobreaker(to))
	switch {
	case from == gobreaker.StateClosed && to == gobreaker.StateOpen:
		s.logger.Warn("redis circuit breaker opened: rate-limit decisions are degraded",
			slog.String("failure_mode", string(s.mode)),
			slog.Duration("retry_in", s.cooldown))
	case to == gobreaker.StateClosed:
		s.logger.Info("redis circuit breaker closed: rate-limit decisions use redis again",
			slog.Int("degraded_decisions_since_last_warning", s.warn.reset()))
	default:
		s.logger.Debug("redis circuit breaker changed state",
			slog.String("from", from.String()), slog.String("to", to.String()))
	}
}

func fromGobreaker(st gobreaker.State) BreakerState {
	switch st {
	case gobreaker.StateHalfOpen:
		return BreakerHalfOpen
	case gobreaker.StateOpen:
		return BreakerOpen
	default:
		return BreakerClosed
	}
}

// warnLimiter spaces out the degraded WARN: the first degraded decision
// after a quiet period is logged at once, then at most one line per every,
// each with the number of degraded decisions it covers.
type warnLimiter struct {
	every time.Duration
	now   func() time.Time

	mu      sync.Mutex
	last    time.Time
	pending int
}

// tick counts one degraded decision, and reports whether to log now and
// how many decisions the line covers.
func (w *warnLimiter) tick() (bool, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending++
	now := w.now()
	if !w.last.IsZero() && now.Sub(w.last) < w.every {
		return false, 0
	}
	n := w.pending
	w.pending, w.last = 0, now
	return true, n
}

// reset starts a quiet period, so the next outage is logged at once, and
// returns how many decisions no line has reported yet.
func (w *warnLimiter) reset() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.pending
	w.pending, w.last = 0, time.Time{}
	return n
}

type nopObserver struct{}

func (nopObserver) DegradedDecision(FailureMode)     {}
func (nopObserver) RedisError()                      {}
func (nopObserver) BreakerStateChanged(BreakerState) {}
