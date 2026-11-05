package analytics

import (
	"context"
	_ "embed" // for the usage scripts
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

//go:embed scripts/add.lua
var addScript string

//go:embed scripts/drain.lua
var drainScript string

const (
	// usageKeyPrefix starts each minute's hash: rg:usage:<minute>, the
	// minute as Unix seconds at its start.
	usageKeyPrefix = "rg:usage:"

	// usageIndexKey is a sorted set of the minutes that have a hash, scored
	// by the minute, so the rollup finds them oldest first without SCAN.
	usageIndexKey = "rg:usage:minutes"

	// unidentified stands for callers identified only by IP in a field
	// name. Client IDs are UUIDs, so it cannot clash with one.
	unidentified = "-"

	fieldAllowed  = "a"
	fieldRejected = "r"

	// countsPerScript bounds one add script's work, so a large batch never
	// holds Redis up for long.
	countsPerScript = 250
)

// redisClient is what RedisPending needs from go-redis. *redis.Client
// satisfies it.
type redisClient interface {
	redis.Scripter
	ZRange(ctx context.Context, key string, start, stop int64) *redis.StringSliceCmd
}

// RedisPending keeps flushed usage counts in Redis until a rollup drains
// them: one hash per minute, with a field per caller and outcome. It
// satisfies Sink and Pending, and is safe for concurrent use.
type RedisPending struct {
	rdb   redisClient
	add   *redis.Script
	drain *redis.Script
}

var _ Pending = (*RedisPending)(nil)

// NewRedisPending returns a RedisPending on rdb. The client should have
// retries disabled, so that an add whose reply was lost is not applied
// twice by go-redis itself.
func NewRedisPending(rdb redisClient) *RedisPending {
	return &RedisPending{rdb: rdb, add: redis.NewScript(addScript), drain: redis.NewScript(drainScript)}
}

// Add implements Sink. Each minute's counts go in batches of at most
// countsPerScript, each one atomic. On error, the counts of the failed
// batch and every batch after it are returned. A batch whose reply was
// lost to a timeout may have been applied, so a retry can, rarely, count
// it twice.
func (p *RedisPending) Add(ctx context.Context, counts []domain.UsageCount) ([]domain.UsageCount, error) {
	batches := batchByMinute(counts)
	for i, b := range batches {
		args := make([]any, 0, 1+4*len(b))
		minute := strconv.FormatInt(b[0].Minute.Unix(), 10)
		args = append(args, minute)
		for _, c := range b {
			caller := callerField(c.ClientID)
			if c.Allowed != 0 {
				args = append(args, caller+":"+fieldAllowed, c.Allowed)
			}
			if c.Rejected != 0 {
				args = append(args, caller+":"+fieldRejected, c.Rejected)
			}
		}
		keys := []string{usageKeyPrefix + minute, usageIndexKey}
		if err := p.add.Run(ctx, p.rdb, keys, args...).Err(); err != nil {
			var unsent []domain.UsageCount
			for _, rest := range batches[i:] {
				unsent = append(unsent, rest...)
			}
			return unsent, fmt.Errorf("analytics: adding usage to redis: %w", err)
		}
	}
	return nil, nil
}

// errMalformed means Redis held a usage field or value this package did not
// write.
var errMalformed = errors.New("malformed usage entry")

// Drain implements Pending.
func (p *RedisPending) Drain(ctx context.Context, maxMinutes int) ([]domain.UsageCount, error) {
	if maxMinutes < 1 {
		return nil, nil
	}
	minutes, err := p.rdb.ZRange(ctx, usageIndexKey, 0, int64(maxMinutes)-1).Result()
	if err != nil {
		return nil, fmt.Errorf("analytics: listing pending usage minutes: %w", err)
	}

	var (
		counts    []domain.UsageCount
		malformed int
	)
	for _, m := range minutes {
		sec, err := strconv.ParseInt(m, 10, 64)
		if err != nil {
			// The index entry is not one of ours. Leave it: deleting by a
			// name built from it could hit an unrelated key.
			malformed++
			continue
		}
		fields, err := p.drain.Run(ctx, p.rdb, []string{usageKeyPrefix + m, usageIndexKey}, m).StringSlice()
		if err != nil {
			return counts, fmt.Errorf("analytics: draining usage minute %s: %w", m, err)
		}
		got, bad := parseMinute(time.Unix(sec, 0).UTC(), fields)
		counts = append(counts, got...)
		malformed += bad
	}
	if malformed > 0 {
		return counts, fmt.Errorf("analytics: %w: %d skipped", errMalformed, malformed)
	}
	return counts, nil
}

// batchByMinute splits counts into batches of one minute and at most
// countsPerScript counts each, keeping their order.
func batchByMinute(counts []domain.UsageCount) [][]domain.UsageCount {
	var (
		batches [][]domain.UsageCount
		current []domain.UsageCount
	)
	for _, c := range counts {
		if len(current) > 0 && (!c.Minute.Equal(current[0].Minute) || len(current) == countsPerScript) {
			batches = append(batches, current)
			current = nil
		}
		current = append(current, c)
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func callerField(clientID string) string {
	if clientID == "" {
		return unidentified
	}
	return clientID
}

// parseMinute reads a drained hash, field, value, field, value, ..., into
// one count per caller. It returns how many entries it could not read.
func parseMinute(minute time.Time, fields []string) ([]domain.UsageCount, int) {
	byCaller := make(map[string]*domain.UsageCount, len(fields)/4+1)
	var order []string
	malformed := 0
	for i := 0; i+1 < len(fields); i += 2 {
		caller, kind, ok := cutLast(fields[i], ':')
		n, err := strconv.ParseInt(fields[i+1], 10, 64)
		if !ok || caller == "" || err != nil || n < 0 || (kind != fieldAllowed && kind != fieldRejected) {
			malformed++
			continue
		}
		c, seen := byCaller[caller]
		if !seen {
			clientID := caller
			if caller == unidentified {
				clientID = ""
			}
			c = &domain.UsageCount{ClientID: clientID, Minute: minute}
			byCaller[caller] = c
			order = append(order, caller)
		}
		if kind == fieldAllowed {
			c.Allowed += n
		} else {
			c.Rejected += n
		}
	}
	if len(fields)%2 != 0 {
		malformed++
	}
	counts := make([]domain.UsageCount, 0, len(order))
	for _, caller := range order {
		counts = append(counts, *byCaller[caller])
	}
	return counts, malformed
}

// cutLast splits s around the last sep.
func cutLast(s string, sep byte) (before, after string, ok bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}
