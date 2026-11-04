//go:build integration

package integration

import (
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Rimaz17/RateGuard/internal/limiter"
)

// These tests run the bucket script on a real Redis: Lua number formatting,
// TIME, HMGET of missing fields and PEXPIRE are exactly what a fake would get
// wrong.

// hourly refills one token an hour, so refill during a test is negligible.
var hourly = limiter.Limit{Capacity: 10, RefillRate: 1.0 / 3600}

func newRedisStore(t *testing.T) (*limiter.Redis, *redis.Client) {
	t.Helper()
	_, url := startRedis(t.Context(), t)
	rdb := newRedisClient(t, url)
	return limiter.NewRedis(rdb), rdb
}

func take(t *testing.T, s limiter.Store, key string, l limiter.Limit) limiter.Decision {
	t.Helper()
	d, err := s.Take(t.Context(), key, l)
	if err != nil {
		t.Fatalf("Take(%s): %v", key, err)
	}
	return d
}

// bucketState reads the stored hash: the balance and Redis's time of the
// last refill. ok is false when the key does not exist.
func bucketState(t *testing.T, rdb *redis.Client, key string) (limiter.Bucket, bool) {
	t.Helper()
	vals, err := rdb.HMGet(t.Context(), key, "tokens", "last_refill_us").Result()
	if err != nil {
		t.Fatalf("HMGET %s: %v", key, err)
	}
	if vals[0] == nil && vals[1] == nil {
		return limiter.Bucket{}, false
	}
	tokens, err := strconv.ParseFloat(vals[0].(string), 64)
	if err != nil {
		t.Fatalf("stored tokens %v: %v", vals[0], err)
	}
	us, err := strconv.ParseInt(vals[1].(string), 10, 64)
	if err != nil {
		t.Fatalf("stored last_refill_us %v: %v", vals[1], err)
	}
	return limiter.Bucket{Tokens: tokens, LastRefill: time.UnixMicro(us)}, true
}

func TestRedisStoreBurstThenRefuse(t *testing.T) {
	s, _ := newRedisStore(t)
	const key = "rg:bucket:all:client:burst"

	for i := range hourly.Capacity {
		d := take(t, s, key, hourly)
		if !d.Allowed || d.Limit != 10 || d.Remaining != 9-i || d.RetryAfter != 0 {
			t.Fatalf("take %d = %+v, want allowed with %d remaining", i+1, d, 9-i)
		}
	}
	d := take(t, s, key, hourly)
	if d.Allowed || d.Remaining != 0 {
		t.Fatalf("take 11 = %+v, want refused", d)
	}
	// A whole token takes an hour; a few seconds of the test may have
	// refilled part of one already.
	if d.RetryAfter <= 59*time.Minute || d.RetryAfter > time.Hour {
		t.Errorf("RetryAfter = %v, want just under an hour", d.RetryAfter)
	}
	if d.ResetAfter <= 9*time.Hour+59*time.Minute || d.ResetAfter > 10*time.Hour {
		t.Errorf("ResetAfter = %v, want just under 10 hours", d.ResetAfter)
	}
}

// TestRedisStoreAgreesWithTheEngine replays every allowed take through the
// in-memory engine. An allowed take stores Redis's time as the last refill,
// so the engine can be given exactly the state and clock the script had,
// and must reach the same decision and the same new state.
func TestRedisStoreAgreesWithTheEngine(t *testing.T) {
	s, rdb := newRedisStore(t)
	const key = "rg:bucket:all:client:differential"
	// Fast enough that the sleeps below refill fractions of tokens.
	l := limiter.Limit{Capacity: 5, RefillRate: 40}

	var prev limiter.Bucket
	var allowed, refused int
	for i := range 60 {
		d := take(t, s, key, l)
		state, ok := bucketState(t, rdb, key)
		if !ok {
			t.Fatalf("take %d: no state stored", i)
		}
		if d.Allowed {
			allowed++
		} else {
			refused++
		}
		if !d.Allowed {
			// A refusal writes nothing, so the stored state is the last one.
			if state != prev {
				t.Fatalf("take %d was refused but changed the state from %+v to %+v", i, prev, state)
			}
		} else {
			wantState, want := limiter.Take(prev, l, state.LastRefill)
			if math.Abs(wantState.Tokens-state.Tokens) > 1e-9 || !wantState.LastRefill.Equal(state.LastRefill) {
				t.Fatalf("take %d: Redis stored %+v, the engine computes %+v", i, state, wantState)
			}
			if d.Allowed != want.Allowed || d.Remaining != want.Remaining || d.Limit != want.Limit ||
				!within(d.ResetAfter, want.ResetAfter, time.Microsecond) || d.RetryAfter != want.RetryAfter {
				t.Fatalf("take %d: Redis decided %+v, the engine decides %+v", i, d, want)
			}
			prev = state
		}
		// Mostly faster than the refill, with a pause now and then, so
		// both allowed and refused takes occur.
		if i%7 == 6 {
			time.Sleep(60 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if allowed == 0 || refused == 0 {
		t.Errorf("%d allowed and %d refused takes; the comparison needs both", allowed, refused)
	}
	t.Logf("compared %d allowed takes with the engine; %d refused takes left the state alone", allowed, refused)
}

func within(a, b, tolerance time.Duration) bool {
	d := a - b
	return d >= -tolerance && d <= tolerance
}

func TestRedisStoreRefillsOnRedisTime(t *testing.T) {
	s, _ := newRedisStore(t)
	const key = "rg:bucket:all:client:refill"
	l := limiter.Limit{Capacity: 2, RefillRate: 10} // a token every 100ms

	take(t, s, key, l)
	take(t, s, key, l)
	d := take(t, s, key, l)
	if d.Allowed {
		t.Fatalf("third take = %+v, want refused", d)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > 100*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want at most 100ms", d.RetryAfter)
	}
	time.Sleep(d.RetryAfter)
	if d := take(t, s, key, l); !d.Allowed {
		t.Errorf("after waiting RetryAfter the take = %+v, want allowed (ADR 0011)", d)
	}
}

func TestRedisStoreExpiresKeysWhenFull(t *testing.T) {
	s, rdb := newRedisStore(t)
	const key = "rg:bucket:all:client:ttl"
	l := limiter.Limit{Capacity: 10, RefillRate: 1} // full again a second per token taken

	take(t, s, key, l)
	ttl, err := rdb.PTTL(t.Context(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 900*time.Millisecond || ttl > time.Second {
		t.Errorf("PTTL after one take = %v, want about 1s: the time until the bucket is full", ttl)
	}

	take(t, s, key, l)
	take(t, s, key, l)
	if ttl, _ = rdb.PTTL(t.Context(), key).Result(); ttl <= 2900*time.Millisecond || ttl > 3*time.Second {
		t.Errorf("PTTL after three takes = %v, want about 3s", ttl)
	}

	// A very slow rate is capped rather than overflowing PEXPIRE. The raw
	// reply is read because the cap, in nanoseconds, overflows a Duration.
	slow := limiter.Limit{Capacity: 1_000_000, RefillRate: 1e-12}
	take(t, s, key+":slow", slow)
	ms, err := rdb.Do(t.Context(), "PTTL", key+":slow").Int64()
	if err != nil {
		t.Fatal(err)
	}
	const yearMillis = 365 * 24 * 3600 * 1000
	if ms < yearMillis || ms > 1e13 {
		t.Errorf("PTTL for a bucket that takes eons to refill = %d ms, want between a year and the 10^13 ms cap", ms)
	}
}

func TestRedisStorePeekAndRefusalWriteNothing(t *testing.T) {
	s, rdb := newRedisStore(t)
	const key = "rg:bucket:all:client:readonly"
	l := limiter.Limit{Capacity: 1, RefillRate: 1.0 / 3600}

	d, err := s.Peek(t.Context(), key, l)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.Remaining != 1 {
		t.Errorf("Peek of a new bucket = %+v, want allowed with 1 remaining", d)
	}
	if n, _ := rdb.Exists(t.Context(), key).Result(); n != 0 {
		t.Fatal("Peek of a new bucket created its key")
	}

	take(t, s, key, l)
	before, _ := bucketState(t, rdb, key)
	ttlBefore, _ := rdb.PTTL(t.Context(), key).Result()
	if d := take(t, s, key, l); d.Allowed {
		t.Fatalf("second take = %+v, want refused", d)
	}
	if d, _ := s.Peek(t.Context(), key, l); d.Allowed || d.Remaining != 0 {
		t.Errorf("Peek of an empty bucket = %+v, want refused with 0 remaining", d)
	}
	after, _ := bucketState(t, rdb, key)
	ttlAfter, _ := rdb.PTTL(t.Context(), key).Result()
	if after != before || ttlAfter > ttlBefore {
		t.Errorf("a refusal and a peek changed the bucket: %+v (ttl %v) became %+v (ttl %v)", before, ttlBefore, after, ttlAfter)
	}
}

func TestRedisStoreClampsALoweredCapacity(t *testing.T) {
	s, _ := newRedisStore(t)
	const key = "rg:bucket:all:client:downgrade"

	take(t, s, key, limiter.Limit{Capacity: 100, RefillRate: 1.0 / 3600})
	d := take(t, s, key, limiter.Limit{Capacity: 10, RefillRate: 1.0 / 3600})
	if !d.Allowed || d.Limit != 10 || d.Remaining != 9 {
		t.Errorf("after lowering the capacity to 10 the take = %+v, want 9 remaining", d)
	}
}

// Redis's clock can go backwards, for example after a failover. The stored
// last refill is then ahead of TIME, and the script must refill nothing and
// count the lag in its durations (ADR 0011).
func TestRedisStoreHandlesABackwardsClock(t *testing.T) {
	s, rdb := newRedisStore(t)
	const key = "rg:bucket:all:client:backwards"
	l := limiter.Limit{Capacity: 4, RefillRate: 2}

	now, err := rdb.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	ahead := now.Add(5 * time.Second).UnixMicro()
	rdb.HSet(t.Context(), key, "tokens", "0", "last_refill_us", strconv.FormatInt(ahead, 10))

	d := take(t, s, key, l)
	if d.Allowed {
		t.Fatalf("take = %+v, want refused: nothing may refill while the clock is behind", d)
	}
	// One token takes 0.5s once the clock catches up in about 5s.
	if d.RetryAfter < 5*time.Second || d.RetryAfter > 5500*time.Millisecond {
		t.Errorf("RetryAfter = %v, want the lag plus 0.5s", d.RetryAfter)
	}
	if d.ResetAfter < 6*time.Second || d.ResetAfter > 7*time.Second {
		t.Errorf("ResetAfter = %v, want the lag plus 2s", d.ResetAfter)
	}
	state, _ := bucketState(t, rdb, key)
	if state.LastRefill.UnixMicro() != ahead {
		t.Errorf("last refill moved from %d to %d; it must stay put", ahead, state.LastRefill.UnixMicro())
	}
}

func TestRedisStoreTreatsCorruptStateAsEmpty(t *testing.T) {
	s, rdb := newRedisStore(t)
	l := limiter.Limit{Capacity: 3, RefillRate: 1.0 / 3600}
	now, err := rdb.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	last := strconv.FormatInt(now.UnixMicro(), 10)

	for name, tokens := range map[string]string{"nan": "nan", "garbage": "lots", "negative": "-7"} {
		key := "rg:bucket:all:client:corrupt-" + name
		rdb.HSet(t.Context(), key, "tokens", tokens, "last_refill_us", last)
		if d := take(t, s, key, l); d.Allowed {
			t.Errorf("%s balance: take = %+v, want refused as an empty bucket", name, d)
		}
	}
	// A bucket without a usable last refill time is a new, full bucket.
	key := "rg:bucket:all:client:corrupt-time"
	rdb.HSet(t.Context(), key, "tokens", "0", "last_refill_us", "never")
	if d := take(t, s, key, l); !d.Allowed || d.Remaining != 2 {
		t.Errorf("unusable time: take = %+v, want allowed from a full bucket", d)
	}
}

func TestRedisStoreReloadsAFlushedScript(t *testing.T) {
	s, rdb := newRedisStore(t)
	const key = "rg:bucket:all:client:noscript"

	take(t, s, key, hourly)
	if err := rdb.ScriptFlush(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	if d := take(t, s, key, hourly); !d.Allowed || d.Remaining != 8 {
		t.Errorf("take after SCRIPT FLUSH = %+v, want allowed with 8 remaining", d)
	}
}

func TestRedisStoreRefusesInvalidLimits(t *testing.T) {
	s, _ := newRedisStore(t)
	for _, l := range []limiter.Limit{{Capacity: 0, RefillRate: 1}, {Capacity: 1, RefillRate: 0}, {Capacity: 1, RefillRate: math.Inf(1)}} {
		if _, err := s.Take(t.Context(), "rg:bucket:all:client:invalid", l); !errors.Is(err, limiter.ErrInvalidLimit) {
			t.Errorf("Take with %+v: error = %v, want ErrInvalidLimit", l, err)
		}
	}
}

// TestRedisStoreIsExactUnderConcurrency is the shared limit in miniature:
// several store instances, each with its own connection pool like separate
// RateGuard processes, hammer one key, and exactly the capacity is allowed.
func TestRedisStoreIsExactUnderConcurrency(t *testing.T) {
	_, url := startRedis(t.Context(), t)
	const (
		instances = 3
		workers   = 16
		perWorker = 50
		key       = "rg:bucket:all:client:contended"
	)
	l := limiter.Limit{Capacity: 1000, RefillRate: 1.0 / 3600}

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range instances {
		s := limiter.NewRedis(newRedisClient(t, url))
		for range workers {
			wg.Go(func() {
				for range perWorker {
					d, err := s.Take(t.Context(), key, l)
					if err != nil {
						t.Errorf("Take: %v", err)
						return
					}
					if d.Allowed {
						allowed.Add(1)
					}
				}
			})
		}
	}
	wg.Wait()
	// 2,400 attempts on 1,000 tokens; a whole extra token needs an hour.
	if got := allowed.Load(); got != int64(l.Capacity) {
		t.Errorf("allowed %d of %d takes, want exactly %d", got, instances*workers*perWorker, l.Capacity)
	}
}
