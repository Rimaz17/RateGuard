package postgres

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rimaz17/RateGuard/internal/domain"
)

// usageRowsPerStatement bounds one upsert, so a large backlog is written in
// several statements of one transaction.
const usageRowsPerStatement = 5000

// Usage stores per-minute usage counts in usage_rollups and reports on them
// (ADR 0018). Only the rollup and the analytics endpoint use it, never a
// decision.
type Usage struct {
	pool *pgxpool.Pool
}

// NewUsage returns a usage store on pool.
func NewUsage(pool *pgxpool.Pool) *Usage {
	return &Usage{pool: pool}
}

// AddUsage adds counts to the stored ones, in one transaction. Counts for
// the same client and minute are summed first, since one statement cannot
// update a row twice. Counts of a client that no longer exists are
// discarded: they could only fail the whole batch, forever.
func (u *Usage) AddUsage(ctx context.Context, counts []domain.UsageCount) error {
	rows := mergeCounts(counts)
	if len(rows) == 0 {
		return nil
	}
	err := pgx.BeginFunc(ctx, u.pool, func(tx pgx.Tx) error {
		for chunk := range slices.Chunk(rows, usageRowsPerStatement) {
			if err := upsertUsage(ctx, tx, chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("adding usage: %w", err)
	}
	return nil
}

func upsertUsage(ctx context.Context, tx pgx.Tx, rows []domain.UsageCount) error {
	var (
		clients  = make([]*string, len(rows))
		minutes  = make([]time.Time, len(rows))
		allowed  = make([]int64, len(rows))
		rejected = make([]int64, len(rows))
	)
	for i, r := range rows {
		if r.ClientID != "" {
			clients[i] = &r.ClientID
		}
		minutes[i] = r.Minute.UTC()
		allowed[i] = r.Allowed
		rejected[i] = r.Rejected
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO usage_rollups (client_id, bucket_start, allowed_count, rejected_count)
		SELECT u.client_id, u.bucket_start, u.allowed, u.rejected
		FROM unnest($1::uuid[], $2::timestamptz[], $3::bigint[], $4::bigint[])
			AS u(client_id, bucket_start, allowed, rejected)
		WHERE u.client_id IS NULL OR EXISTS (SELECT 1 FROM clients c WHERE c.id = u.client_id)
		ON CONFLICT (client_id, bucket_start) DO UPDATE SET
			allowed_count = usage_rollups.allowed_count + EXCLUDED.allowed_count,
			rejected_count = usage_rollups.rejected_count + EXCLUDED.rejected_count`,
		clients, minutes, allowed, rejected)
	if err != nil {
		return fmt.Errorf("upserting usage rows: %w", err)
	}
	return nil
}

// mergeCounts sums counts by client and minute, dropping empty ones and any
// whose client ID is not an ID at all.
func mergeCounts(counts []domain.UsageCount) []domain.UsageCount {
	type key struct {
		clientID string
		minute   int64
	}
	index := make(map[key]int, len(counts))
	merged := make([]domain.UsageCount, 0, len(counts))
	for _, c := range counts {
		if c.Allowed == 0 && c.Rejected == 0 {
			continue
		}
		if c.ClientID != "" && !domain.IsID(c.ClientID) {
			continue
		}
		k := key{c.ClientID, c.Minute.Unix()}
		if i, ok := index[k]; ok {
			merged[i].Allowed += c.Allowed
			merged[i].Rejected += c.Rejected
			continue
		}
		index[k] = len(merged)
		merged = append(merged, c)
	}
	return merged
}

// Usage reports on [q.From, q.To) from one consistent snapshot. Its series
// is sparse: only points with checks appear. q must already be validated;
// analytics.Reporter does that.
func (u *Usage) Usage(ctx context.Context, q domain.UsageQuery) (domain.UsageReport, error) {
	var rep domain.UsageReport
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	err := pgx.BeginTxFunc(ctx, u.pool, opts, func(tx pgx.Tx) error {
		var err error
		if rep.Totals, rep.Unidentified, err = usageTotals(ctx, tx, q); err != nil {
			return err
		}
		if rep.Clients, err = usageByClient(ctx, tx, q); err != nil {
			return err
		}
		rep.Series, err = usageSeries(ctx, tx, q)
		return err
	})
	if err != nil {
		return domain.UsageReport{}, fmt.Errorf("reporting usage: %w", err)
	}
	return rep, nil
}

func usageTotals(ctx context.Context, tx pgx.Tx, q domain.UsageQuery) (all, unidentified domain.UsageTotals, err error) {
	err = tx.QueryRow(ctx, `
		SELECT
			coalesce(sum(allowed_count), 0)::bigint,
			coalesce(sum(rejected_count), 0)::bigint,
			coalesce(sum(allowed_count) FILTER (WHERE client_id IS NULL), 0)::bigint,
			coalesce(sum(rejected_count) FILTER (WHERE client_id IS NULL), 0)::bigint
		FROM usage_rollups
		WHERE bucket_start >= $1 AND bucket_start < $2`,
		q.From, q.To).Scan(&all.Allowed, &all.Rejected, &unidentified.Allowed, &unidentified.Rejected)
	if err != nil {
		return all, unidentified, fmt.Errorf("summing usage: %w", err)
	}
	return all, unidentified, nil
}

func usageByClient(ctx context.Context, tx pgx.Tx, q domain.UsageQuery) ([]domain.ClientUsage, error) {
	rows, err := tx.Query(ctx, `
		SELECT u.client_id::text, c.name,
			sum(u.allowed_count)::bigint, sum(u.rejected_count)::bigint
		FROM usage_rollups u
		JOIN clients c ON c.id = u.client_id
		WHERE u.bucket_start >= $1 AND u.bucket_start < $2
		GROUP BY u.client_id, c.name
		ORDER BY sum(u.rejected_count) DESC,
			sum(u.allowed_count) + sum(u.rejected_count) DESC,
			u.client_id
		LIMIT $3`,
		q.From, q.To, q.Top)
	if err != nil {
		return nil, fmt.Errorf("ranking clients by usage: %w", err)
	}
	clients, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ClientUsage, error) {
		var c domain.ClientUsage
		err := row.Scan(&c.ClientID, &c.Name, &c.Allowed, &c.Rejected)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("ranking clients by usage: %w", err)
	}
	return clients, nil
}

func usageSeries(ctx context.Context, tx pgx.Tx, q domain.UsageQuery) ([]domain.UsagePoint, error) {
	// date_trunc with an explicit zone does not depend on the session's
	// TimeZone. The granularity is minute or hour, checked by the caller,
	// and goes in as a parameter rather than into the SQL text.
	rows, err := tx.Query(ctx, `
		SELECT date_trunc($3::text, bucket_start, 'UTC') AS start,
			sum(allowed_count)::bigint, sum(rejected_count)::bigint
		FROM usage_rollups
		WHERE bucket_start >= $1 AND bucket_start < $2
		GROUP BY start
		ORDER BY start`,
		q.From, q.To, string(q.Granularity))
	if err != nil {
		return nil, fmt.Errorf("reading usage series: %w", err)
	}
	series, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.UsagePoint, error) {
		var p domain.UsagePoint
		err := row.Scan(&p.Start, &p.Allowed, &p.Rejected)
		p.Start = p.Start.UTC()
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading usage series: %w", err)
	}
	return series, nil
}
