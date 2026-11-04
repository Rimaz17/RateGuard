-- Registered applications that call RateGuard, such as a customer app or a
-- merchant app.
--
-- policy_id is NULL until a policy is assigned. The foreign key keeps the
-- default NO ACTION rule: policies are disabled rather than deleted, so
-- deleting one that clients still use is a mistake worth an error.
CREATE TABLE clients (
    id         UUID        NOT NULL DEFAULT uuidv7(),
    name       TEXT        NOT NULL,
    policy_id  UUID,
    status     TEXT        NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT clients_pkey PRIMARY KEY (id),
    CONSTRAINT clients_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES policies (id),
    CONSTRAINT clients_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT clients_name_length CHECK (char_length(name) <= 100),
    CONSTRAINT clients_status_valid CHECK (status IN ('active', 'disabled'))
);

-- Postgres does not index the referencing side of a foreign key. Without
-- this, finding a policy's clients (for cache invalidation) or checking the
-- key when a policy row is deleted would scan the whole table.
CREATE INDEX clients_policy_id_idx ON clients (policy_id);
