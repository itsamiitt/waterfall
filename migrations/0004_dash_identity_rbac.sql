-- Migration 0004 — dashboard identity, RBAC, and audit evidence (ADR-0018, ADR-0020; doc 03 §2.1, doc 05).
--
-- Adds the second GUC accessor app_current_role() (ADR-0020 dual-GUC model): both
-- app.current_tenant and app.current_role are bound per transaction by the internal/dash/db
-- tx helper from the verified Principal — never from request bodies (G1 tenant isolation).
-- Seeds the reserved sentinel Tenant 'platform' (signup only creates kind='customer').
--
-- audit_log is the per-Tenant SHA-256 hash chain (hash = sha256(prev_hash || canonical_json)),
-- serialized by an audit_chain_heads row lock (doc 03 §9.2). RANGE-partitioned by year;
-- never deleted. api_access_log is written by an async batch inserter (never blocks requests).
--
-- Partitions are created by the runtime partition maintainer, whose ensure pass runs
-- synchronously at dashboardd startup (doc 03 §4) — migrations create parents and a DEFAULT
-- backstop partition only. Applied atomically by internal/pgmigrate; no BEGIN/COMMIT here.
--
-- secret_envelopes + the users.mfa_totp_envelope_id FK live here by Deviation D-1 (doc 12 P0):
-- P0's MFA enroll/verify must seal/open totp_seed envelopes (doc 05 §5.2), so the sealed store
-- ships in 0004 alongside users instead of the deferred 0005 add.

-- Helper: the current request role for this transaction, from the session setting (ADR-0020).
CREATE OR REPLACE FUNCTION app_current_role() RETURNS text
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.current_role', /* missing_ok = */ true)
$$;

-- ---------------------------------------------------------------------------
-- tenants — Tenant registry, including the sentinel 'platform' row.
-- ---------------------------------------------------------------------------
CREATE TABLE tenants (
    id         text PRIMARY KEY CHECK (id ~ '^[a-z0-9-]{1,64}$'),
    name       text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('platform', 'customer')),
    plan_tier  text,
    status     text NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Seed BEFORE enabling RLS: with FORCE RLS and no GUC bound, the insert below would be blocked.
INSERT INTO tenants (id, name, kind, plan_tier, status)
VALUES ('platform', 'Platform', 'platform', 'internal', 'active');

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
-- Isolation policy keyed on id (this table's PK IS the tenant id): a Tenant sees and manages
-- only itself. Operators get the enumerated cross-tenant SELECT policy below — SELECT-ONLY,
-- like every other operator cross-tenant policy (ADR-0020; doc 05 §3.3 footnote 6: WITH CHECK
-- blocks operator cross-tenant writes). There is deliberately NO operator write path here:
-- tenant lifecycle writes (signup, plan/status changes) happen via the signup/provisioning
-- path outside this surface, in a transaction bound to the affected Tenant's own id (doc 05
-- SEC-3); an operator Principal writes only the 'platform' row (its own tenant scope).
CREATE POLICY tenants_tenant_isolation ON tenants
    USING (id = app_current_tenant())
    WITH CHECK (id = app_current_tenant());
CREATE POLICY tenants_operator_read ON tenants
    FOR SELECT USING (app_current_role() = 'operator');

-- ---------------------------------------------------------------------------
-- users — human logins (RBAC principals). password_hash format: pbkdf2-sha256$<iters>$<salt>$<dk>,
-- sha256, 600k iterations (doc 05). mfa_totp_envelope_id references secret_envelopes (below).
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id                   uuid PRIMARY KEY,
    tenant_id            text NOT NULL REFERENCES tenants(id),
    email                text NOT NULL,
    password_hash        text NOT NULL,
    role                 text NOT NULL CHECK (role IN ('operator', 'tenant_admin', 'tenant_user')),
    abac                 jsonb,               -- {region, plan_tier, ...} attribute checks (doc 05)
    mfa_totp_envelope_id uuid,                -- FK to secret_envelopes added below
    mfa_enrolled_at      timestamptz,
    status               text NOT NULL DEFAULT 'active',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_tenant_email_uq ON users (tenant_id, lower(email));

-- ---------------------------------------------------------------------------
-- mfa_recovery_codes — hashed one-time recovery codes. tenant_id is present because this is
-- a Class T table (ADR-0020); it is NEVER operator-readable.
-- ---------------------------------------------------------------------------
CREATE TABLE mfa_recovery_codes (
    tenant_id text  NOT NULL,
    user_id   uuid  NOT NULL REFERENCES users(id),
    code_hash bytea NOT NULL,
    used_at   timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

-- ---------------------------------------------------------------------------
-- sessions — browser sessions (ADR-0018). id is 256-bit random, base64url. Reaped by the
-- session-reaper loop 24h after expiry. NEVER operator-readable across Tenants.
-- ---------------------------------------------------------------------------
CREATE TABLE sessions (
    id                  text PRIMARY KEY,
    tenant_id           text NOT NULL,
    user_id             uuid NOT NULL REFERENCES users(id),
    csrf_token          text NOT NULL,
    ip                  inet,
    user_agent          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    idle_expires_at     timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    mfa_verified_at     timestamptz,
    revoked_at          timestamptz
);
CREATE INDEX sessions_tenant_user_idx ON sessions (tenant_id, user_id);
CREATE INDEX sessions_idle_expiry_idx ON sessions (idle_expires_at);

-- ---------------------------------------------------------------------------
-- ip_allowlists — per-Tenant CIDR allowlists enforced by httpx middleware (doc 05).
-- ---------------------------------------------------------------------------
CREATE TABLE ip_allowlists (
    id         uuid PRIMARY KEY,
    tenant_id  text NOT NULL,
    cidr       cidr NOT NULL,
    label      text,
    created_by uuid,                -- users.id; soft reference (users are never hard-deleted)
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- audit_log — per-Tenant SHA-256 hash chain; append-only evidence; never deleted.
-- hash = sha256(prev_hash || canonical_json(entry)); (tenant_id, seq) uniqueness is
-- guaranteed by the audit_chain_heads row-lock serialization (§9.2), not by a global
-- unique index (a partitioned table cannot enforce uniqueness without the partition key).
-- id uses an explicit sequence: identity columns on partitioned parents require PG 17;
-- DEFAULT nextval() is version-safe.
-- ---------------------------------------------------------------------------
CREATE SEQUENCE audit_log_id_seq;
CREATE TABLE audit_log (
    id            bigint NOT NULL DEFAULT nextval('audit_log_id_seq'),
    tenant_id     text   NOT NULL,
    seq           bigint NOT NULL,
    actor_user_id uuid,
    actor_role    text,
    action        text   NOT NULL,
    object_kind   text,
    object_id     text,
    before        jsonb,
    after         jsonb,
    ip            inet,
    prev_hash     bytea  NOT NULL,
    hash          bytea  NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;
CREATE INDEX audit_log_chain_idx ON audit_log (tenant_id, seq);

-- audit_chain_heads — one row per Tenant chain; SELECT ... FOR UPDATE on this row
-- serializes appends per Tenant (§9.2).
CREATE TABLE audit_chain_heads (
    tenant_id text  PRIMARY KEY,
    last_seq  bigint NOT NULL DEFAULT 0,
    last_hash bytea  NOT NULL
);

-- ---------------------------------------------------------------------------
-- api_access_log — request telemetry, written by the async batch inserter; monthly
-- partitions, 90d retention (§4). No secrets, no PII beyond ip.
-- ---------------------------------------------------------------------------
CREATE SEQUENCE api_access_log_id_seq;
CREATE TABLE api_access_log (
    id         bigint NOT NULL DEFAULT nextval('api_access_log_id_seq'),
    tenant_id  text   NOT NULL,
    user_id    uuid,
    method     text   NOT NULL,
    route      text   NOT NULL,
    status     int    NOT NULL,
    dur_ms     int    NOT NULL,
    ip         inet,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE TABLE api_access_log_default PARTITION OF api_access_log DEFAULT;
CREATE INDEX api_access_log_tenant_idx ON api_access_log (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- FORCE RLS + 0001-style tenant isolation on every Class T table above (tenants already has
-- its isolation policy, keyed on id). Enumerated operator read-projections follow.
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['users', 'mfa_recovery_codes', 'sessions', 'ip_allowlists',
                             'audit_log', 'audit_chain_heads', 'api_access_log'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Operator cross-tenant SELECT: ONLY the ADR-0020 enumerated list. Every handler that
-- serves a cross-tenant operator view writes an audit_log row.
CREATE POLICY users_operator_read ON users
    FOR SELECT USING (app_current_role() = 'operator');
CREATE POLICY audit_log_operator_read ON audit_log
    FOR SELECT USING (app_current_role() = 'operator');

-- ---------------------------------------------------------------------------
-- secret_envelopes — Deviation D-1 (doc 12 P0): pulled forward from 0005 into 0004 because P0's
-- MFA enroll/verify must seal/open totp_seed envelopes (doc 05 §5.2). Class P (platform-only RLS,
-- no tenant policy ever); only internal/dash/secrets reads it (one-owner-per-table). No plaintext
-- column exists anywhere. AAD on Open = envelope_id || kind; aad_fingerprint is a KEYED
-- HMAC-SHA256(fingerprint_pepper, plaintext) used for provider_key duplicate detection (P1).
-- ---------------------------------------------------------------------------
CREATE TABLE secret_envelopes (
    id              uuid PRIMARY KEY,
    kind            text NOT NULL CHECK (kind IN
                    ('provider_key', 'totp_seed', 'webhook_secret', 'channel_config')),
    master_key_id   text  NOT NULL,     -- keyring entry that wraps this DEK (KEK rotation)
    dek_wrapped     bytea NOT NULL,
    nonce           bytea NOT NULL,
    ciphertext      bytea NOT NULL,
    aad_fingerprint bytea,              -- HMAC-SHA256(fingerprint_pepper, plaintext); KEYED
    created_at      timestamptz NOT NULL DEFAULT now(),
    rotated_from    uuid REFERENCES secret_envelopes(id)
);
CREATE INDEX secret_envelopes_fingerprint_idx ON secret_envelopes (aad_fingerprint);
ALTER TABLE secret_envelopes ENABLE ROW LEVEL SECURITY;
ALTER TABLE secret_envelopes FORCE ROW LEVEL SECURITY;
CREATE POLICY secret_envelopes_platform_only ON secret_envelopes
    USING (app_current_tenant() = 'platform')
    WITH CHECK (app_current_tenant() = 'platform');

-- users.mfa_totp_envelope_id FK, inline now that secret_envelopes lives in this migration.
ALTER TABLE users ADD CONSTRAINT users_mfa_envelope_fk
    FOREIGN KEY (mfa_totp_envelope_id) REFERENCES secret_envelopes(id);
