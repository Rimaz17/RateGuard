package domain

import "time"

// UsageCount is how many of one caller's checks were allowed and rejected
// in one minute. It is the unit the usage pipeline moves from the hot path
// to the usage_rollups table (ADR 0018).
type UsageCount struct {
	// ClientID is the client the checks were made for, or "" for callers
	// identified only by IP. Addresses are never recorded.
	ClientID string

	// Minute is the start of the minute, in UTC.
	Minute time.Time

	Allowed  int64
	Rejected int64
}

// Granularity is the width of one point in a usage series.
type Granularity string

// Granularities a usage series can have.
const (
	GranularityMinute Granularity = "minute"
	GranularityHour   Granularity = "hour"
)

// Duration returns the width of one point.
func (g Granularity) Duration() time.Duration {
	switch g {
	case GranularityMinute:
		return time.Minute
	case GranularityHour:
		return time.Hour
	default:
		return 0
	}
}

// UsageQuery asks for usage over [From, To), with the series in points of
// Granularity and the Top clients ranked by rejections.
type UsageQuery struct {
	From        time.Time
	To          time.Time
	Granularity Granularity
	Top         int
}

// UsageTotals counts checks by outcome.
type UsageTotals struct {
	Allowed  int64
	Rejected int64
}

// Requests is every check counted: allowed plus rejected.
func (t UsageTotals) Requests() int64 { return t.Allowed + t.Rejected }

// ClientUsage is one client's totals over a usage report's range.
type ClientUsage struct {
	ClientID string
	Name     string
	UsageTotals
}

// UsagePoint is the totals of one point in a usage series, starting at
// Start.
type UsagePoint struct {
	Start time.Time
	UsageTotals
}

// UsageReport answers a UsageQuery.
type UsageReport struct {
	// Query is the query as answered: defaults applied, and the range
	// widened to whole points of its granularity.
	Query UsageQuery

	// Totals counts every check in the range.
	Totals UsageTotals

	// Unidentified counts the checks of callers identified only by IP.
	Unidentified UsageTotals

	// Clients ranks registered clients by rejections, most first, then by
	// requests. It holds at most Query.Top clients, and only clients with
	// checks in the range.
	Clients []ClientUsage

	// Series holds one point per Granularity from Query.From to Query.To,
	// zeros included.
	Series []UsagePoint
}
