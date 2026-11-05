-- Per-minute usage: how many checks each caller had allowed and rejected.
-- Counts are buffered in each instance, gathered in Redis and added here by
-- the rollup (ADR 0018), so no decision ever waits on this table.
--
-- client_id is NULL for callers identified only by IP: their checks are
-- counted together, and no address is stored. NULLS NOT DISTINCT makes that
-- one row per minute too, so the rollup's upsert can add to it.
--
-- bucket_start is the start of a minute. The check reads it in UTC, where
-- a minute boundary does not depend on the session's time zone.
CREATE TABLE usage_rollups (
    id             UUID        NOT NULL DEFAULT uuidv7(),
    client_id      UUID,
    bucket_start   TIMESTAMPTZ NOT NULL,
    allowed_count  BIGINT      NOT NULL DEFAULT 0,
    rejected_count BIGINT      NOT NULL DEFAULT 0,

    CONSTRAINT usage_rollups_pkey PRIMARY KEY (id),
    CONSTRAINT usage_rollups_client_id_fkey FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE,
    CONSTRAINT usage_rollups_client_minute_key UNIQUE NULLS NOT DISTINCT (client_id, bucket_start),
    CONSTRAINT usage_rollups_counts_non_negative CHECK (allowed_count >= 0 AND rejected_count >= 0),
    CONSTRAINT usage_rollups_bucket_is_minute CHECK (
        date_trunc('minute', bucket_start AT TIME ZONE 'UTC') = bucket_start AT TIME ZONE 'UTC'
    )
);

-- The analytics endpoint reads a time range across every client. The unique
-- constraint's index leads with client_id, so it cannot serve that.
CREATE INDEX usage_rollups_bucket_start_idx ON usage_rollups (bucket_start);
