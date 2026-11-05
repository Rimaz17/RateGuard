package analytics_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/analytics"
	"github.com/Rimaz17/RateGuard/internal/domain"
)

// source records the query it was asked and answers with a fixed report.
type source struct {
	asked  domain.UsageQuery
	report domain.UsageReport
	err    error
}

func (s *source) Usage(_ context.Context, q domain.UsageQuery) (domain.UsageReport, error) {
	s.asked = q
	return s.report, s.err
}

func TestReporterNormalizesTheQuery(t *testing.T) {
	now := at("2026-11-05T10:17:30Z")
	tests := []struct {
		name string
		in   domain.UsageQuery
		want domain.UsageQuery
	}{
		{
			name: "defaults: the last day by the hour, top 10",
			in:   domain.UsageQuery{},
			want: domain.UsageQuery{From: at("2026-11-04T11:00:00Z"), To: at("2026-11-05T11:00:00Z"), Granularity: domain.GranularityHour, Top: 10},
		},
		{
			name: "defaults by the minute: exactly a day, however far into the minute now is",
			in:   domain.UsageQuery{Granularity: domain.GranularityMinute},
			want: domain.UsageQuery{From: at("2026-11-04T10:18:00Z"), To: at("2026-11-05T10:18:00Z"), Granularity: domain.GranularityMinute, Top: 10},
		},
		{
			name: "widened to whole minutes",
			in:   domain.UsageQuery{From: at("2026-11-05T10:00:30Z"), To: at("2026-11-05T10:05:01Z"), Granularity: domain.GranularityMinute, Top: 3},
			want: domain.UsageQuery{From: at("2026-11-05T10:00:00Z"), To: at("2026-11-05T10:06:00Z"), Granularity: domain.GranularityMinute, Top: 3},
		},
		{
			name: "aligned bounds stay put",
			in:   domain.UsageQuery{From: at("2026-11-05T08:00:00Z"), To: at("2026-11-05T10:00:00Z")},
			want: domain.UsageQuery{From: at("2026-11-05T08:00:00Z"), To: at("2026-11-05T10:00:00Z"), Granularity: domain.GranularityHour, Top: 10},
		},
		{
			name: "only from: up to now",
			in:   domain.UsageQuery{From: at("2026-11-05T09:00:00Z"), Granularity: domain.GranularityMinute},
			want: domain.UsageQuery{From: at("2026-11-05T09:00:00Z"), To: at("2026-11-05T10:18:00Z"), Granularity: domain.GranularityMinute, Top: 10},
		},
		{
			name: "only to: the day before it",
			in:   domain.UsageQuery{To: at("2026-11-03T00:00:00Z")},
			want: domain.UsageQuery{From: at("2026-11-02T00:00:00Z"), To: at("2026-11-03T00:00:00Z"), Granularity: domain.GranularityHour, Top: 10},
		},
		{
			name: "hours fall on UTC boundaries whatever the zone",
			in:   domain.UsageQuery{From: at("2026-11-05T15:45:00+05:30"), To: at("2026-11-05T16:45:00+05:30")},
			want: domain.UsageQuery{From: at("2026-11-05T10:00:00Z"), To: at("2026-11-05T12:00:00Z"), Granularity: domain.GranularityHour, Top: 10},
		},
		{
			name: "the longest series allowed",
			in:   domain.UsageQuery{From: at("2026-11-04T00:00:00Z"), To: at("2026-11-05T00:00:00Z"), Granularity: domain.GranularityMinute, Top: 100},
			want: domain.UsageQuery{From: at("2026-11-04T00:00:00Z"), To: at("2026-11-05T00:00:00Z"), Granularity: domain.GranularityMinute, Top: 100},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &source{}
			rep, err := analytics.NewReporter(src, func() time.Time { return now }).Usage(t.Context(), tt.in)
			if err != nil {
				t.Fatalf("Usage: %v", err)
			}
			for name, got := range map[string]domain.UsageQuery{"asked": src.asked, "reported": rep.Query} {
				if !got.From.Equal(tt.want.From) || !got.To.Equal(tt.want.To) || got.Granularity != tt.want.Granularity || got.Top != tt.want.Top {
					t.Errorf("%s %+v\nwant  %+v", name, got, tt.want)
				}
				if got.From.Location() != time.UTC || got.To.Location() != time.UTC {
					t.Errorf("%s range is not in UTC: %v, %v", name, got.From, got.To)
				}
			}
		})
	}
}

func TestReporterRejectsBadQueries(t *testing.T) {
	tests := []struct {
		name  string
		in    domain.UsageQuery
		field string
	}{
		{"unknown granularity", domain.UsageQuery{Granularity: "week"}, "granularity"},
		{"top too large", domain.UsageQuery{Top: 101}, "top"},
		{"top negative", domain.UsageQuery{Top: -1}, "top"},
		{"from after to", domain.UsageQuery{From: at("2026-11-05T11:00:00Z"), To: at("2026-11-05T10:00:00Z")}, "from"},
		{"empty range", domain.UsageQuery{From: at("2026-11-05T10:00:00Z"), To: at("2026-11-05T10:00:00Z")}, "from"},
		{"a minute over a day by the minute", domain.UsageQuery{From: at("2026-11-04T00:00:00Z"), To: at("2026-11-05T00:00:01Z"), Granularity: domain.GranularityMinute}, "from"},
		{"over 60 days by the hour", domain.UsageQuery{From: at("2026-01-01T00:00:00Z"), To: at("2026-11-01T00:00:00Z")}, "from"},
		{"centuries apart", domain.UsageQuery{From: time.Date(2, 1, 1, 0, 0, 0, 0, time.UTC), To: at("2026-11-01T00:00:00Z")}, "from"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &source{}
			_, err := analytics.NewReporter(src, nil).Usage(t.Context(), tt.in)
			var ve *domain.ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field {
				t.Fatalf("Usage(%+v) = %v, want a validation error on %s", tt.in, err, tt.field)
			}
			if !src.asked.From.IsZero() {
				t.Error("an invalid query reached the source")
			}
		})
	}
}

func TestReporterFillsTheSeries(t *testing.T) {
	src := &source{report: domain.UsageReport{
		Totals: domain.UsageTotals{Allowed: 7, Rejected: 2},
		Series: []domain.UsagePoint{
			{Start: at("2026-11-05T10:01:00Z"), UsageTotals: domain.UsageTotals{Allowed: 5}},
			{Start: at("2026-11-05T10:03:00Z"), UsageTotals: domain.UsageTotals{Allowed: 2, Rejected: 2}},
		},
	}}
	q := domain.UsageQuery{From: at("2026-11-05T10:00:00Z"), To: at("2026-11-05T10:04:00Z"), Granularity: domain.GranularityMinute}
	rep, err := analytics.NewReporter(src, nil).Usage(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		start             string
		allowed, rejected int64
	}{
		{"2026-11-05T10:00:00Z", 0, 0},
		{"2026-11-05T10:01:00Z", 5, 0},
		{"2026-11-05T10:02:00Z", 0, 0},
		{"2026-11-05T10:03:00Z", 2, 2},
	}
	if len(rep.Series) != len(want) {
		t.Fatalf("series has %d points, want %d: %+v", len(rep.Series), len(want), rep.Series)
	}
	for i, w := range want {
		p := rep.Series[i]
		if !p.Start.Equal(at(w.start)) || p.Allowed != w.allowed || p.Rejected != w.rejected {
			t.Errorf("point %d = %v %+v, want %s allowed %d rejected %d", i, p.Start, p.UsageTotals, w.start, w.allowed, w.rejected)
		}
	}
	if rep.Totals.Requests() != 9 {
		t.Errorf("totals %+v, want the source's", rep.Totals)
	}
}

func TestReporterPassesSourceErrorsOn(t *testing.T) {
	boom := errors.New("connection refused")
	_, err := analytics.NewReporter(&source{err: boom}, nil).Usage(t.Context(), domain.UsageQuery{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "reading usage") {
		t.Fatalf("Usage = %v, want the source's error wrapped", err)
	}
}
