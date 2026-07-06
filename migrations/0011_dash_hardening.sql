-- Migration 0011 — hardening closeout: TOTP replay guard, durable admin idempotency,
-- per-rule anomaly floor (doc 03 §2.8; closes OI-SEC-8, OI-API-8, OI-P6-3).

-- ---------------------------------------------------------------------------
-- mfa_used_steps — TOTP single-use guard (doc 05 §5.1, SEC-1/OI-SEC-8): a (user, time_step)
-- is accepted at most once, so a captured code cannot be replayed inside its ±1-step window.
-- Class T; NEVER operator-readable. Retention: reaped after 10 minutes (forensic slack only —
-- correctness needs ~90s); reaper shares the session-reaper loop.
-- ---------------------------------------------------------------------------
CREATE TABLE mfa_used_steps (
    tenant_id text   NOT NULL,
    user_id   uuid   NOT NULL REFERENCES users(id),
    time_step bigint NOT NULL,             -- floor(unix/30) accepted
    used_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, time_step)       -- INSERT ... ON CONFLICT DO NOTHING; zero rows = replay
);
CREATE INDEX mfa_used_steps_reap_idx ON mfa_used_steps (used_at);

-- ---------------------------------------------------------------------------
-- dash_admin_idempotency — durable ledger for /v1/admin writes (doc 04 §1.3; replaces the
-- P0 in-process map, Deviation D-P0-2/OI-API-8). First writer wins; a replay with the same
-- body_hash returns the stored response with Idempotency-Replayed: true; a different
-- body_hash → 409 idempotency_key_reuse. Class T. Retention: 24h reap.
-- ---------------------------------------------------------------------------
CREATE TABLE dash_admin_idempotency (
    tenant_id       text  NOT NULL,
    idempotency_key text  NOT NULL,
    body_hash       bytea NOT NULL,        -- sha256 of the request body
    status          int,                   -- stored response (NULL while first request in flight)
    response        jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, idempotency_key)
);
CREATE INDEX dash_admin_idempotency_reap_idx ON dash_admin_idempotency (created_at);

-- Per-rule absolute anomaly floor (doc 10 §4 cost.anomaly; OI-P6-3): NULL = the package
-- default (1000 credits). Additive column on a small config table — online-safe.
ALTER TABLE alert_rules ADD COLUMN anomaly_floor_credits bigint;

-- Class T isolation for the two new tables (0001-style).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['mfa_used_steps', 'dash_admin_idempotency'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;
