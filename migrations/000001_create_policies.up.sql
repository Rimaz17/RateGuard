-- Rate-limit policies such as Free, Premium and Enterprise.
--
-- Policies are disabled (enabled = false), never deleted, so a client can
-- never reference a missing row. uuidv7() keys are time-ordered, which keeps
-- the primary key index compact and gives keyset pagination a stable order.
CREATE TABLE policies (
    id             UUID             NOT NULL DEFAULT uuidv7(),
    name           TEXT             NOT NULL,
    capacity       INTEGER          NOT NULL,
    window_seconds INTEGER          NOT NULL,
    refill_rate    DOUBLE PRECISION NOT NULL,
    enabled        BOOLEAN          NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ      NOT NULL DEFAULT now(),

    CONSTRAINT policies_pkey PRIMARY KEY (id),
    CONSTRAINT policies_name_key UNIQUE (name),
    CONSTRAINT policies_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT policies_name_length CHECK (char_length(name) <= 100),
    CONSTRAINT policies_capacity_positive CHECK (capacity > 0),
    CONSTRAINT policies_window_seconds_positive CHECK (window_seconds > 0),
    -- Postgres sorts NaN above every number, so "> 0" alone would admit NaN;
    -- the upper bound rejects both NaN and Infinity.
    CONSTRAINT policies_refill_rate_finite_positive CHECK (refill_rate > 0 AND refill_rate < 'Infinity')
);
