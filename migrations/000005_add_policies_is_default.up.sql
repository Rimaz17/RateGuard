-- The default policy applies to callers with no enabled policy of their
-- own, including callers identified only by IP (ADR 0009).
--
-- The partial unique index admits any number of FALSE rows but only one
-- TRUE row, so at most one policy is the default. Moving the flag clears the
-- old holder first, in the same transaction.
ALTER TABLE policies ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT FALSE;

CREATE UNIQUE INDEX policies_one_default_idx ON policies (is_default) WHERE is_default;
