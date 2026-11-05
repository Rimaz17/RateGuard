package limiter

import (
	"context"
	_ "embed" // for the bucket script
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed scripts/bucket.lua
var bucketScript string

// Script modes, ARGV[3] of the bucket script.
const (
	modeTake = "take"
	modePeek = "peek"
)

// Redis is a Store that keeps buckets in Redis, so that every instance draws
// from the same ones (ADR 0013). Each decision is one atomic run of the
// bucket script, which reads Redis's clock (ADR 0002) and returns relative
// values; the decision is then built exactly as Memory builds it.
//
// A Redis is safe for concurrent use. It reports Redis failures as errors
// and does nothing else about them: degrading is the job of the circuit
// breaker wrapped around it (ADR 0001).
type Redis struct {
	rdb    redis.Scripter
	script *redis.Script
}

var _ Store = (*Redis)(nil)

// NewRedis returns a Redis store on rdb. The client should have retries
// disabled: a script that ran but whose reply was lost would take a second
// token if retried (ADR 0013).
func NewRedis(rdb redis.Scripter) *Redis {
	return &Redis{rdb: rdb, script: redis.NewScript(bucketScript)}
}

// Take implements Store. An invalid limit is refused with ErrInvalidLimit
// before Redis is called.
func (s *Redis) Take(ctx context.Context, key string, l Limit) (Decision, error) {
	return s.run(ctx, key, l, modeTake)
}

// Peek implements Store. It never writes to Redis.
func (s *Redis) Peek(ctx context.Context, key string, l Limit) (Decision, error) {
	return s.run(ctx, key, l, modePeek)
}

func (s *Redis) run(ctx context.Context, key string, l Limit, mode string) (Decision, error) {
	if err := checkRequest(ctx, l); err != nil {
		return Decision{}, err
	}
	// The rate is passed in its shortest exact form, so the script
	// computes with exactly the float Go has.
	reply, err := s.script.Run(ctx, s.rdb, []string{key},
		l.Capacity, strconv.FormatFloat(l.RefillRate, 'g', -1, 64), mode).Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: running the bucket script: %w", err)
	}
	allowed, tokens, lag, err := parseReply(reply)
	if err != nil {
		return Decision{}, fmt.Errorf("limiter: bucket script reply: %w", err)
	}
	// The script reports where the bucket stands relative to Redis's clock,
	// never the clock itself. Any fixed instant can stand in for "now".
	var now time.Time
	return decide(Bucket{Tokens: tokens, LastRefill: now.Add(lag)}, l, now, allowed), nil
}

var errBadReply = errors.New("unexpected reply")

// parseReply reads {allowed, tokens, lag_us} as the bucket script returns
// it.
func parseReply(reply []any) (allowed bool, tokens float64, lag time.Duration, err error) {
	if len(reply) != 3 {
		return false, 0, 0, fmt.Errorf("%w: %d values, want 3", errBadReply, len(reply))
	}
	flag, ok := reply[0].(int64)
	if !ok || (flag != 0 && flag != 1) {
		return false, 0, 0, fmt.Errorf("%w: allowed is %v", errBadReply, reply[0])
	}
	tokens, err = parseNumber(reply[1])
	if err != nil || !(tokens >= 0) || math.IsInf(tokens, 1) {
		return false, 0, 0, fmt.Errorf("%w: tokens is %v", errBadReply, reply[1])
	}
	lagMicros, err := parseNumber(reply[2])
	if err != nil || !(lagMicros >= 0) {
		return false, 0, 0, fmt.Errorf("%w: lag is %v", errBadReply, reply[2])
	}
	return flag == 1, tokens, microseconds(lagMicros), nil
}

func parseNumber(v any) (float64, error) {
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("%w: %T is not a string", errBadReply, v)
	}
	return strconv.ParseFloat(s, 64)
}

// microseconds converts a non-negative count of microseconds to a Duration,
// rounding up and saturating at the largest Duration like the engine does.
func microseconds(us float64) time.Duration {
	nanos := math.Ceil(us * float64(time.Microsecond))
	if !(nanos < maxDurationNanos) { // also catches +Inf
		return math.MaxInt64
	}
	return time.Duration(nanos)
}
