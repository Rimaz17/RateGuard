-- API keys, kept apart from clients so a client can hold two valid keys at
-- once and rotate without downtime: issue new, migrate callers, revoke old
-- (ADR 0006).
--
-- Only the SHA-256 digest of a key is stored. key_prefix is the public part
-- (rg_<tag>_<16 random base32 chars>); its unique index is the lookup on the
-- authentication path. A revoked key keeps its row, so the admin API can
-- still show when it was revoked.
CREATE TABLE api_keys (
    id         UUID        NOT NULL DEFAULT uuidv7(),
    client_id  UUID        NOT NULL,
    key_prefix TEXT        NOT NULL,
    key_hash   BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ,

    CONSTRAINT api_keys_pkey PRIMARY KEY (id),
    CONSTRAINT api_keys_client_id_fkey FOREIGN KEY (client_id) REFERENCES clients (id) ON DELETE CASCADE,
    CONSTRAINT api_keys_key_prefix_key UNIQUE (key_prefix),
    CONSTRAINT api_keys_key_prefix_format CHECK (key_prefix ~ '^rg_[a-z0-9]{1,3}_[a-z2-7]{16}$'),
    CONSTRAINT api_keys_key_hash_sha256 CHECK (octet_length(key_hash) = 32),
    CONSTRAINT api_keys_revoked_after_created CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

-- Listing a client's keys and counting its active ones.
CREATE INDEX api_keys_client_id_idx ON api_keys (client_id);
