package analytics

import (
	"context"
	"fmt"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// Query bounds and defaults (ADR 0018).
const (
	// DefaultRange is how far back a query reaches when it names no start.
	DefaultRange = 24 * time.Hour

	// DefaultTop is how many clients a report ranks when the query does
	// not say.
	DefaultTop = 10

	// MaxTop is the most clients a report ranks.
	MaxTop = 100

	// MaxPoints is the longest series a report holds: a day by the minute,
	// or 60 days by the hour.
	MaxPoints = 1440
)

// Source reads usage from the usage_rollups table. *postgres.Usage
// satisfies it. Its report's Series may be sparse: points with no checks
// can be left out.
type Source interface {
	Usage(ctx context.Context, q domain.UsageQuery) (domain.UsageReport, error)
}

// Reporter answers usage queries: it fills in defaults, checks the query,
// and returns a series with a point for every interval in the range.
type Reporter struct {
	src Source
	now func() time.Time
}

// NewReporter returns a Reporter reading from src. now supplies the default
// end of a query's range, or time.Now when nil.
func NewReporter(src Source, now func() time.Time) *Reporter {
	if now == nil {
		now = time.Now
	}
	return &Reporter{src: src, now: now}
}

// Usage answers q. Zero fields take defaults: the hour granularity, a range
// ending now, starting DefaultRange before its (widened) end, and DefaultTop
// clients. The range is widened to whole points: From down and To up to the
// granularity, in UTC. An invalid query fails with a *domain.ValidationError.
func (r *Reporter) Usage(ctx context.Context, q domain.UsageQuery) (domain.UsageReport, error) {
	q, err := r.normalize(q)
	if err != nil {
		return domain.UsageReport{}, err
	}
	rep, err := r.src.Usage(ctx, q)
	if err != nil {
		return domain.UsageReport{}, fmt.Errorf("reading usage: %w", err)
	}
	rep.Query = q
	rep.Series = fillSeries(q, rep.Series)
	return rep, nil
}

func (r *Reporter) normalize(q domain.UsageQuery) (domain.UsageQuery, error) {
	if q.Granularity == "" {
		q.Granularity = domain.GranularityHour
	}
	step := q.Granularity.Duration()
	if step == 0 {
		return q, &domain.ValidationError{Field: "granularity", Reason: "must be minute or hour"}
	}
	if q.Top == 0 {
		q.Top = DefaultTop
	}
	if q.Top < 1 || q.Top > MaxTop {
		return q, &domain.ValidationError{Field: "top", Reason: fmt.Sprintf("must be between 1 and %d", MaxTop)}
	}
	if q.To.IsZero() {
		q.To = r.now()
	}
	if !q.From.IsZero() && !q.From.Before(q.To) {
		return q, &domain.ValidationError{Field: "from", Reason: "must be before to"}
	}

	// Truncate works on absolute time, so minutes and hours fall on UTC
	// boundaries whatever zone the caller wrote the times in.
	end := q.To.Truncate(step)
	if end.Before(q.To) {
		end = end.Add(step)
	}
	q.To = end.UTC()
	// The default start is measured from the widened end, so the default
	// range is exactly DefaultRange: a whole number of points either way.
	if q.From.IsZero() {
		q.From = q.To.Add(-DefaultRange)
	}
	q.From = q.From.Truncate(step).UTC()
	// Sub saturates on absurd ranges, which then fail this check too.
	if q.To.Sub(q.From) > MaxPoints*step {
		return q, &domain.ValidationError{
			Field:  "from",
			Reason: fmt.Sprintf("and to must span at most %d points of the granularity (a day by the minute, 60 days by the hour)", MaxPoints),
		}
	}
	return q, nil
}

// fillSeries returns one point per step from q.From to q.To, taking the
// totals of sparse where it has a point.
func fillSeries(q domain.UsageQuery, sparse []domain.UsagePoint) []domain.UsagePoint {
	byStart := make(map[int64]domain.UsageTotals, len(sparse))
	for _, p := range sparse {
		t := byStart[p.Start.Unix()]
		t.Allowed += p.Allowed
		t.Rejected += p.Rejected
		byStart[p.Start.Unix()] = t
	}
	step := q.Granularity.Duration()
	series := make([]domain.UsagePoint, 0, int(q.To.Sub(q.From)/step))
	for t := q.From; t.Before(q.To); t = t.Add(step) {
		series = append(series, domain.UsagePoint{Start: t, UsageTotals: byStart[t.Unix()]})
	}
	return series
}
