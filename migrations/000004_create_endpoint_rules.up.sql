-- Endpoint rules: a policy's stricter (or looser) limit for part of an API,
-- such as 5 requests a minute on /api/login under the Free plan. A matching
-- rule beats its policy's own limit; ADR 0010 defines the pattern syntax and
-- the precedence between rules. The application validates the full pattern
-- syntax; the CHECKs below hold the shape every row must have regardless.
--
-- Rules belong to their policy, so they go with it (ON DELETE CASCADE).
-- Policies are disabled rather than deleted, so in practice that only
-- happens when an operator removes a policy by hand.
CREATE TABLE endpoint_rules (
    id             UUID        NOT NULL DEFAULT uuidv7(),
    policy_id      UUID        NOT NULL,
    method         TEXT        NOT NULL,
    path_pattern   TEXT        NOT NULL,
    capacity       INTEGER     NOT NULL,
    window_seconds INTEGER     NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT endpoint_rules_pkey PRIMARY KEY (id),
    CONSTRAINT endpoint_rules_policy_id_fkey FOREIGN KEY (policy_id) REFERENCES policies (id) ON DELETE CASCADE,
    -- One rule per method and pattern within a policy. The index leads with
    -- policy_id, so it also serves loading a policy's rules.
    CONSTRAINT endpoint_rules_policy_method_pattern_key UNIQUE (policy_id, method, path_pattern),
    CONSTRAINT endpoint_rules_method_valid CHECK (method = '*' OR method ~ '^[A-Z]{1,16}$'),
    CONSTRAINT endpoint_rules_path_pattern_shape CHECK (left(path_pattern, 1) = '/' AND octet_length(path_pattern) <= 512),
    CONSTRAINT endpoint_rules_capacity_positive CHECK (capacity > 0),
    CONSTRAINT endpoint_rules_window_seconds_positive CHECK (window_seconds > 0)
);
