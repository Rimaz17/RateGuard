package httptransport_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
	httptransport "github.com/Rimaz17/RateGuard/internal/transport/http"
)

// stubReporter answers with a fixed report and records the query.
type stubReporter struct {
	mu     sync.Mutex
	asked  []domain.UsageQuery
	report domain.UsageReport
	err    error
}

func (s *stubReporter) Usage(_ context.Context, q domain.UsageQuery) (domain.UsageReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, q)
	return s.report, s.err
}

func newAnalyticsServer(t *testing.T, rep httptransport.UsageReporter) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httptransport.NewRouter(httptransport.Deps{
		Logger:     slog.New(slog.DiscardHandler),
		AdminToken: adminToken,
		Usage:      rep,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUsageRequiresTheAdminToken(t *testing.T) {
	rep := &stubReporter{}
	srv := newAnalyticsServer(t, rep)
	resp, _ := call(t, srv, http.MethodGet, "/v1/analytics/usage", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if len(rep.asked) != 0 {
		t.Error("the reporter was asked without the admin token")
	}
}

func TestUsageReport(t *testing.T) {
	from := mustTime(t, "2026-11-05T10:00:00Z")
	rep := &stubReporter{report: domain.UsageReport{
		Query:        domain.UsageQuery{From: from, To: from.Add(2 * time.Minute), Granularity: domain.GranularityMinute, Top: 5},
		Totals:       domain.UsageTotals{Allowed: 10, Rejected: 4},
		Unidentified: domain.UsageTotals{Allowed: 1},
		Clients: []domain.ClientUsage{
			{ClientID: "0193f2a0-0000-7000-8000-00000000000a", Name: "Customer App", UsageTotals: domain.UsageTotals{Allowed: 9, Rejected: 4}},
		},
		Series: []domain.UsagePoint{
			{Start: from, UsageTotals: domain.UsageTotals{Allowed: 10, Rejected: 4}},
			{Start: from.Add(time.Minute)},
		},
	}}
	srv := newAnalyticsServer(t, rep)

	resp, body := call(t, srv, http.MethodGet,
		"/v1/analytics/usage?from=2026-11-05T10:00:30Z&to=2026-11-05T11:01:00%2B01:00&granularity=minute&top=5", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	want := `{
		"from": "2026-11-05T10:00:00Z", "to": "2026-11-05T10:02:00Z", "granularity": "minute",
		"totals": {"requests": 14, "allowed": 10, "rejected": 4},
		"unidentified": {"requests": 1, "allowed": 1, "rejected": 0},
		"clients": [{"client_id": "0193f2a0-0000-7000-8000-00000000000a", "name": "Customer App", "requests": 13, "allowed": 9, "rejected": 4}],
		"series": [
			{"start": "2026-11-05T10:00:00Z", "requests": 14, "allowed": 10, "rejected": 4},
			{"start": "2026-11-05T10:01:00Z", "requests": 0, "allowed": 0, "rejected": 0}
		]
	}`
	assertJSONEqual(t, body, want)

	if len(rep.asked) != 1 {
		t.Fatalf("reporter asked %d times, want once", len(rep.asked))
	}
	q := rep.asked[0]
	if !q.From.Equal(mustTime(t, "2026-11-05T10:00:30Z")) || !q.To.Equal(mustTime(t, "2026-11-05T10:01:00Z")) ||
		q.Granularity != domain.GranularityMinute || q.Top != 5 {
		t.Errorf("reporter asked %+v, want the parameters as sent", q)
	}
}

func TestUsageLeavesAbsentParametersToTheReporter(t *testing.T) {
	rep := &stubReporter{}
	srv := newAnalyticsServer(t, rep)
	resp, body := call(t, srv, http.MethodGet, "/v1/analytics/usage", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	if q := rep.asked[0]; !q.From.IsZero() || !q.To.IsZero() || q.Granularity != "" || q.Top != 0 {
		t.Errorf("reporter asked %+v, want a zero query for it to default", q)
	}
	// Empty lists are [] rather than null.
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["clients"].([]any); !ok {
		t.Errorf("clients = %v, want an empty list", got["clients"])
	}
	if _, ok := got["series"].([]any); !ok {
		t.Errorf("series = %v, want an empty list", got["series"])
	}
}

func TestUsageRejectsBadParameters(t *testing.T) {
	tests := []struct {
		query, want string
	}{
		{"from=yesterday", "from must be an RFC 3339 time"},
		{"to=2026-11-05", "to must be an RFC 3339 time"},
		{"top=0", "top must be a whole number of at least 1"},
		{"top=ten", "top must be a whole number of at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rep := &stubReporter{}
			srv := newAnalyticsServer(t, rep)
			resp, body := call(t, srv, http.MethodGet, "/v1/analytics/usage?"+tt.query, "", true)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), tt.want) {
				t.Errorf("status %d, body %s; want 400 containing %q", resp.StatusCode, body, tt.want)
			}
			if len(rep.asked) != 0 {
				t.Error("the reporter was asked about a bad query")
			}
		})
	}
}

func TestUsageReportsValidationErrorsAndHidesOthers(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		status   int
		contains string
	}{
		{"validation", &domain.ValidationError{Field: "granularity", Reason: "must be minute or hour"}, http.StatusBadRequest, "granularity must be minute or hour"},
		{"internal", errors.New("pq: relation usage_rollups does not exist"), http.StatusInternalServerError, "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newAnalyticsServer(t, &stubReporter{err: tt.err})
			resp, body := call(t, srv, http.MethodGet, "/v1/analytics/usage?granularity=week", "", true)
			if resp.StatusCode != tt.status || !strings.Contains(string(body), tt.contains) {
				t.Errorf("status %d, body %s; want %d containing %q", resp.StatusCode, body, tt.status, tt.contains)
			}
			if strings.Contains(string(body), "usage_rollups") {
				t.Errorf("body leaks the internal error: %s", body)
			}
		})
	}
}

func TestUsageRouteIsAbsentWithoutAReporter(t *testing.T) {
	srv := newAnalyticsServer(t, nil)
	resp, _ := call(t, srv, http.MethodGet, "/v1/analytics/usage", "", true)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	gb, _ := json.Marshal(g) // re-encoding decoded JSON cannot fail
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("body\n%s\nwant\n%s", gb, wb)
	}
}
