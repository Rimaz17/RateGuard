package httptransport

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// UsageReporter answers usage queries. *analytics.Reporter satisfies it: it
// fills in defaults and rejects an invalid query with a
// *domain.ValidationError.
type UsageReporter interface {
	Usage(ctx context.Context, q domain.UsageQuery) (domain.UsageReport, error)
}

type analyticsHandlers struct {
	logger   *slog.Logger
	reporter UsageReporter
}

type usageTotalsDTO struct {
	Requests int64 `json:"requests"`
	Allowed  int64 `json:"allowed"`
	Rejected int64 `json:"rejected"`
}

type clientUsageDTO struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
	usageTotalsDTO
}

type usagePointDTO struct {
	Start time.Time `json:"start"`
	usageTotalsDTO
}

// usageResponse is the body of GET /v1/analytics/usage (ADR 0018).
type usageResponse struct {
	From         time.Time        `json:"from"`
	To           time.Time        `json:"to"`
	Granularity  string           `json:"granularity"`
	Totals       usageTotalsDTO   `json:"totals"`
	Unidentified usageTotalsDTO   `json:"unidentified"`
	Clients      []clientUsageDTO `json:"clients"`
	Series       []usagePointDTO  `json:"series"`
}

func toTotalsDTO(t domain.UsageTotals) usageTotalsDTO {
	return usageTotalsDTO{Requests: t.Requests(), Allowed: t.Allowed, Rejected: t.Rejected}
}

func toUsageResponse(rep domain.UsageReport) usageResponse {
	resp := usageResponse{
		From:         rep.Query.From,
		To:           rep.Query.To,
		Granularity:  string(rep.Query.Granularity),
		Totals:       toTotalsDTO(rep.Totals),
		Unidentified: toTotalsDTO(rep.Unidentified),
		Clients:      make([]clientUsageDTO, 0, len(rep.Clients)),
		Series:       make([]usagePointDTO, 0, len(rep.Series)),
	}
	for _, c := range rep.Clients {
		resp.Clients = append(resp.Clients, clientUsageDTO{ClientID: c.ClientID, Name: c.Name, usageTotalsDTO: toTotalsDTO(c.UsageTotals)})
	}
	for _, p := range rep.Series {
		resp.Series = append(resp.Series, usagePointDTO{Start: p.Start, usageTotalsDTO: toTotalsDTO(p.UsageTotals)})
	}
	return resp
}

// usage handles GET /v1/analytics/usage.
func (h analyticsHandlers) usage(w http.ResponseWriter, r *http.Request) {
	q, e := parseUsageQuery(r)
	if e != nil {
		writeRequestError(w, r, e)
		return
	}
	rep, err := h.reporter.Usage(r.Context(), q)
	if err != nil {
		writeServiceError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toUsageResponse(rep))
}

// parseUsageQuery reads from, to, granularity and top. Absent ones stay
// zero, for the reporter to default; it also checks the values' ranges.
func parseUsageQuery(r *http.Request) (domain.UsageQuery, *requestError) {
	v := r.URL.Query()
	var q domain.UsageQuery
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		raw := v.Get(p.name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return q, badRequest("%s must be an RFC 3339 time, such as 2026-11-05T14:00:00Z", p.name)
		}
		*p.dst = t
	}
	q.Granularity = domain.Granularity(v.Get("granularity"))
	if raw := v.Get("top"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return q, badRequest("top must be a whole number of at least 1")
		}
		q.Top = n
	}
	return q, nil
}
