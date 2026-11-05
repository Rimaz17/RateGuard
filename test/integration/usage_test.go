//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Rimaz17/RateGuard/internal/analytics"
	"github.com/Rimaz17/RateGuard/internal/domain"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

func minute(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// usageRow is one row of usage_rollups, client "" for unidentified callers.
type usageRow struct {
	client            string
	start             time.Time
	allowed, rejected int64
}

// TestUsageRollupsAddUp checks the table and the upsert the rollup relies
// on: counts add rather than overwrite, unidentified callers share one row
// per minute, a batch may name a client and minute twice, and counts of a
// client that no longer exists are dropped instead of failing the batch.
func TestUsageRollupsAddUp(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	clients := postgres.NewClients(pool)
	a, err := clients.Create(ctx, "Customer App")
	if err != nil {
		t.Fatal(err)
	}
	usage := postgres.NewUsage(pool)
	m1 := minute(t, "2026-11-05T10:00:00Z")
	gone := "0193f2a0-0000-7000-8000-0000000000ff"

	if err := usage.AddUsage(ctx, []domain.UsageCount{
		{ClientID: a.ID, Minute: m1, Allowed: 3, Rejected: 1},
		{ClientID: a.ID, Minute: m1, Allowed: 1},
		{ClientID: "", Minute: m1, Allowed: 2},
		{ClientID: gone, Minute: m1, Allowed: 5},
	}); err != nil {
		t.Fatalf("first AddUsage: %v", err)
	}
	if err := usage.AddUsage(ctx, []domain.UsageCount{
		{ClientID: a.ID, Minute: m1, Rejected: 2},
		{ClientID: "", Minute: m1, Rejected: 1},
	}); err != nil {
		t.Fatalf("second AddUsage: %v", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT coalesce(client_id::text, ''), bucket_start, allowed_count, rejected_count
		FROM usage_rollups ORDER BY client_id NULLS FIRST`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (usageRow, error) {
		var u usageRow
		err := r.Scan(&u.client, &u.start, &u.allowed, &u.rejected)
		return u, err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []usageRow{
		{client: "", start: m1, allowed: 2, rejected: 1},
		{client: a.ID, start: m1, allowed: 4, rejected: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].client != want[i].client || !got[i].start.Equal(want[i].start) ||
			got[i].allowed != want[i].allowed || got[i].rejected != want[i].rejected {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// A bucket must start on a UTC minute, whatever the session's time
	// zone; Asia/Kolkata is UTC+05:30.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // nothing here is meant to be kept
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Asia/Kolkata'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO usage_rollups (client_id, bucket_start) VALUES (NULL, '2026-11-05T10:01:00Z')`); err != nil {
		t.Errorf("inserting a bucket on a minute: %v", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO usage_rollups (client_id, bucket_start) VALUES (NULL, '2026-11-05T10:02:30Z')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "usage_rollups_bucket_is_minute" {
		t.Errorf("inserting a bucket at :30 seconds: %v, want usage_rollups_bucket_is_minute violated", err)
	}
}

// TestUsageReportFromPostgres runs real rows through the Reporter and the
// store's queries: totals, the unidentified share, clients ranked by
// rejections, and a series by the hour and by the minute.
func TestUsageReportFromPostgres(t *testing.T) {
	ctx := t.Context()
	pool := migratedPool(t)
	clients := postgres.NewClients(pool)
	a, err := clients.Create(ctx, "Customer App")
	if err != nil {
		t.Fatal(err)
	}
	b, err := clients.Create(ctx, "Merchant App")
	if err != nil {
		t.Fatal(err)
	}
	usage := postgres.NewUsage(pool)
	if err := usage.AddUsage(ctx, []domain.UsageCount{
		{ClientID: a.ID, Minute: minute(t, "2026-11-05T09:59:00Z"), Allowed: 100}, // before the range
		{ClientID: a.ID, Minute: minute(t, "2026-11-05T10:00:00Z"), Allowed: 10, Rejected: 1},
		{ClientID: b.ID, Minute: minute(t, "2026-11-05T10:01:00Z"), Allowed: 2, Rejected: 5},
		{ClientID: "", Minute: minute(t, "2026-11-05T10:01:00Z"), Allowed: 7},
		{ClientID: a.ID, Minute: minute(t, "2026-11-05T11:30:00Z"), Allowed: 1, Rejected: 1},
		{ClientID: a.ID, Minute: minute(t, "2026-11-05T12:00:00Z"), Allowed: 100}, // after it
	}); err != nil {
		t.Fatal(err)
	}
	reporter := analytics.NewReporter(usage, nil)

	rep, err := reporter.Usage(ctx, domain.UsageQuery{
		From: minute(t, "2026-11-05T10:00:00Z"), To: minute(t, "2026-11-05T12:00:00Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals != (domain.UsageTotals{Allowed: 20, Rejected: 7}) {
		t.Errorf("totals = %+v, want 20 allowed and 7 rejected", rep.Totals)
	}
	if rep.Unidentified != (domain.UsageTotals{Allowed: 7}) {
		t.Errorf("unidentified = %+v, want 7 allowed", rep.Unidentified)
	}
	if len(rep.Clients) != 2 || rep.Clients[0].ClientID != b.ID || rep.Clients[0].Name != "Merchant App" ||
		rep.Clients[0].UsageTotals != (domain.UsageTotals{Allowed: 2, Rejected: 5}) ||
		rep.Clients[1].ClientID != a.ID || rep.Clients[1].UsageTotals != (domain.UsageTotals{Allowed: 11, Rejected: 2}) {
		t.Errorf("clients = %+v, want Merchant App (5 rejected) ranked above Customer App (2)", rep.Clients)
	}
	if len(rep.Series) != 2 ||
		rep.Series[0].UsageTotals != (domain.UsageTotals{Allowed: 19, Rejected: 6}) ||
		rep.Series[1].UsageTotals != (domain.UsageTotals{Allowed: 1, Rejected: 1}) {
		t.Errorf("hourly series = %+v, want 10:00 with 19/6 and 11:00 with 1/1", rep.Series)
	}

	rep, err = reporter.Usage(ctx, domain.UsageQuery{
		From: minute(t, "2026-11-05T10:00:00Z"), To: minute(t, "2026-11-05T10:03:00Z"),
		Granularity: domain.GranularityMinute, Top: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Clients) != 1 || rep.Clients[0].ClientID != b.ID {
		t.Errorf("top 1 = %+v, want only Merchant App", rep.Clients)
	}
	wantSeries := []domain.UsageTotals{{Allowed: 10, Rejected: 1}, {Allowed: 9, Rejected: 5}, {}}
	if len(rep.Series) != len(wantSeries) {
		t.Fatalf("minute series = %+v, want %d points", rep.Series, len(wantSeries))
	}
	for i, w := range wantSeries {
		if rep.Series[i].UsageTotals != w {
			t.Errorf("minute %d = %+v, want %+v", i, rep.Series[i].UsageTotals, w)
		}
	}
}

// TestRedisPendingDrainsAtomically checks the Redis half of the pipeline:
// counts added by many instances while others drain are each drained
// exactly once, and nothing is left behind.
func TestRedisPendingDrainsAtomically(t *testing.T) {
	ctx := t.Context()
	_, redisURL := startRedis(ctx, t)
	rdb := newRedisClient(t, redisURL)
	p := analytics.NewRedisPending(rdb)
	m1 := minute(t, "2026-11-05T10:00:00Z")
	m2 := minute(t, "2026-11-05T10:01:00Z")

	// More callers in one minute than one script adds at a time.
	var batch []domain.UsageCount
	for i := range 300 {
		batch = append(batch, domain.UsageCount{ClientID: fmt.Sprintf("0193f2a0-0000-7000-8000-%012d", i), Minute: m1, Allowed: 1, Rejected: 2})
	}
	batch = append(batch, domain.UsageCount{Minute: m2, Allowed: 4})
	if unsent, err := p.Add(ctx, batch); err != nil || len(unsent) != 0 {
		t.Fatalf("Add: %d unsent, %v", len(unsent), err)
	}
	got, err := p.Drain(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 301 || sumUsage(got) != (domain.UsageTotals{Allowed: 304, Rejected: 600}) {
		t.Fatalf("drained %d counts totalling %+v, want 301 totalling 304 allowed, 600 rejected", len(got), sumUsage(got))
	}
	if last := got[len(got)-1]; last.ClientID != "" || !last.Minute.Equal(m2) {
		t.Errorf("last count = %+v, want the unidentified callers' minute 10:01, drained oldest first", last)
	}

	// Concurrent adds and drains: every count comes out exactly once.
	const adders, each = 8, 200
	var (
		mu      sync.Mutex
		drained []domain.UsageCount
		wg      sync.WaitGroup
		stop    = make(chan struct{})
	)
	for range 2 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				out, err := p.Drain(ctx, 10)
				if err != nil {
					t.Errorf("Drain: %v", err)
					return
				}
				mu.Lock()
				drained = append(drained, out...)
				mu.Unlock()
			}
		})
	}
	var adds sync.WaitGroup
	for i := range adders {
		adds.Go(func() {
			for j := range each {
				c := domain.UsageCount{ClientID: fmt.Sprintf("0193f2a0-0000-7000-8000-%012d", i), Minute: []time.Time{m1, m2}[j%2], Allowed: 1}
				if _, err := p.Add(ctx, []domain.UsageCount{c}); err != nil {
					t.Errorf("Add: %v", err)
					return
				}
			}
		})
	}
	adds.Wait()
	close(stop)
	wg.Wait()
	rest, err := p.Drain(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	drained = append(drained, rest...)
	if got := sumUsage(drained); got.Allowed != adders*each || got.Rejected != 0 {
		t.Errorf("drained %+v under concurrency, want exactly %d allowed", got, adders*each)
	}
	if keys, err := rdb.Keys(ctx, "rg:usage:*").Result(); err != nil || len(keys) != 0 {
		t.Errorf("keys left behind: %v, %v", keys, err)
	}
}

func TestRedisPendingReturnsWhatItCouldNotAdd(t *testing.T) {
	// Nothing listens on port 1.
	rdb := newRedisClient(t, "redis://127.0.0.1:1/0")
	counts := []domain.UsageCount{
		{ClientID: "", Minute: minute(t, "2026-11-05T10:00:00Z"), Allowed: 1},
		{ClientID: "", Minute: minute(t, "2026-11-05T10:01:00Z"), Rejected: 1},
	}
	unsent, err := analytics.NewRedisPending(rdb).Add(t.Context(), counts)
	if err == nil || len(unsent) != 2 {
		t.Fatalf("Add with Redis unreachable = %d unsent, %v; want both back and an error", len(unsent), err)
	}
}

func sumUsage(counts []domain.UsageCount) domain.UsageTotals {
	var t domain.UsageTotals
	for _, c := range counts {
		t.Allowed += c.Allowed
		t.Rejected += c.Rejected
	}
	return t
}

// TestUsageAnalyticsEndToEnd is phase 7's analytics through the real
// service: checks on two instances are counted in memory, flushed into one
// Redis, rolled up into Postgres by one of them, and reported by the other
// over GET /v1/analytics/usage. Nothing is counted until the rollup.
func TestUsageAnalyticsEndToEnd(t *testing.T) {
	pool := migratedPool(t)
	_, redisURL := startRedis(t.Context(), t)
	o := nodeOptions{redisURL: redisURL, instances: 2, timeout: time.Second, failures: 5, cooldown: time.Second}
	a, b := newNode(t, pool, o), newNode(t, pool, o)

	created := adminJSON(t, a.srv, http.MethodPost, "/v1/policies",
		`{"name":"Hourly 5","capacity":5,"window_seconds":3600,"default":true}`, http.StatusCreated)
	policyID, _ := created["id"].(string)
	customerID, customerKey := registerOn(t, a.srv, "Customer App", policyID)
	merchantID, merchantKey := registerOn(t, a.srv, "Merchant App", policyID)

	nodes := []*node{a, b}
	codes := map[int]int{}
	for i := range 8 { // 5 allowed, 3 refused
		codes[postCheck(t, nodes[i%2].srv.URL+"/v1/check", customerKey)]++
	}
	for i := range 2 {
		codes[postCheck(t, nodes[i%2].srv.URL+"/v1/check", merchantKey)]++
	}
	for i := range 3 { // keyless: known by IP, on the default policy
		codes[postCheck(t, nodes[i%2].srv.URL+"/v1/check", "")]++
	}
	postCheck(t, a.srv.URL+"/v1/peek", customerKey) // peeks are not usage
	if codes[200] != 10 || codes[429] != 3 {
		t.Fatalf("statuses = %v, want 10 allowed and 3 refused", codes)
	}

	report := func() usageReport {
		t.Helper()
		from := url.QueryEscape(time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339))
		code, body := adminCall(t, b.srv, http.MethodGet, "/v1/analytics/usage?granularity=minute&from="+from, "")
		if code != http.StatusOK {
			t.Fatalf("GET /v1/analytics/usage = %d %s", code, body)
		}
		var r usageReport
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatalf("decoding %s: %v", body, err)
		}
		return r
	}

	a.flusher.Flush(t.Context())
	b.flusher.Flush(t.Context())
	if r := report(); r.Totals.Requests != 0 {
		t.Fatalf("before any rollup the report shows %+v; usage must reach Postgres only through the rollup", r.Totals)
	}

	a.rollup.Roll(t.Context())
	r := report()
	if r.Totals != (usageTotals{Requests: 13, Allowed: 10, Rejected: 3}) {
		t.Errorf("totals = %+v, want 13 requests: 10 allowed, 3 rejected", r.Totals)
	}
	if r.Unidentified != (usageTotals{Requests: 3, Allowed: 3}) {
		t.Errorf("unidentified = %+v, want the 3 keyless checks", r.Unidentified)
	}
	if len(r.Clients) != 2 ||
		r.Clients[0].ClientID != customerID || r.Clients[0].usageTotals != (usageTotals{Requests: 8, Allowed: 5, Rejected: 3}) ||
		r.Clients[1].ClientID != merchantID || r.Clients[1].usageTotals != (usageTotals{Requests: 2, Allowed: 2}) {
		t.Errorf("clients = %+v, want Customer App (3 rejected) first, then Merchant App", r.Clients)
	}
	var series usageTotals
	for _, p := range r.Series {
		series.Requests += p.Requests
		series.Allowed += p.Allowed
		series.Rejected += p.Rejected
	}
	if series != r.Totals {
		t.Errorf("the series adds up to %+v, want the totals %+v", series, r.Totals)
	}

	// The rollup drained Redis: rolling up again adds nothing.
	b.rollup.Roll(t.Context())
	if again := report(); again.Totals != r.Totals {
		t.Errorf("after a second rollup totals = %+v, want them unchanged at %+v", again.Totals, r.Totals)
	}

	// The metrics count the same checks, and time every decision request.
	var allowed, rejected, timed float64
	for _, n := range nodes {
		allowed += n.metric(t, `rateguard_decisions_total{outcome="allowed"}`)
		rejected += n.metric(t, `rateguard_decisions_total{outcome="rejected"}`)
		timed += n.metric(t, `rateguard_decision_duration_seconds_count{operation="check"}`)
	}
	if allowed != 10 || rejected != 3 || timed != 13 {
		t.Errorf("metrics: %v allowed, %v rejected, %v checks timed; want 10, 3 and 13", allowed, rejected, timed)
	}
	if peeks := a.metric(t, `rateguard_decision_duration_seconds_count{operation="peek"}`); peeks != 1 {
		t.Errorf("peeks timed = %v, want 1", peeks)
	}
}

type usageTotals struct {
	Requests int64 `json:"requests"`
	Allowed  int64 `json:"allowed"`
	Rejected int64 `json:"rejected"`
}

type usageReport struct {
	Totals       usageTotals `json:"totals"`
	Unidentified usageTotals `json:"unidentified"`
	Clients      []struct {
		ClientID string `json:"client_id"`
		Name     string `json:"name"`
		usageTotals
	} `json:"clients"`
	Series []struct {
		Start time.Time `json:"start"`
		usageTotals
	} `json:"series"`
}

// postCheck sends one decision request, with an API key unless key is "",
// and returns the status.
func postCheck(t *testing.T, endpoint, key string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint,
		strings.NewReader(`{"method":"GET","path":"/api/products"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
