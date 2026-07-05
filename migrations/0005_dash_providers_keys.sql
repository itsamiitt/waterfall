-- Migration 0005 — Provider catalog, Provider Keys, Key Pools, secret envelopes (doc 03 §2.2,
-- ADR-0009 inclusion trichotomy, ADR-0017 envelope encryption, ADR-0020 Class P RLS).
--
-- providers.status is the ADR-0009 catalog lifecycle (ACTIVE-CANDIDATE / DEPRIORITIZED /
-- EXCLUDED) and is DISTINCT from runtime op_state (enabled/disabled/paused/maintenance).
-- Effective availability is COMPUTED (providers.EffectiveAvailability), never stored.
--
-- secret_envelopes is the ONLY place ciphertext lives; consuming tables store envelope ids.
-- No plaintext column exists anywhere in the schema. aad_fingerprint is the KEYED duplicate-
-- detection fingerprint HMAC-SHA256(fingerprint_pepper, plaintext) — keyed, never bare
-- SHA-256, so a leaked row cannot be brute-forced against low-entropy vendor keys; the AES-GCM
-- AAD binds envelope id || kind to the ciphertext (swap/splice detection). ADR-0017.
--
-- attrs jsonb is presentation-only: anything read by planner/breaker/rotation MUST be a typed
-- column (doc 01 risk register). Applied atomically by internal/pgmigrate; no BEGIN/COMMIT.

-- ---------------------------------------------------------------------------
-- providers — the platform-owned catalog row IS the connector definition: auth_* columns
-- serialize provider.AuthDescriptor; capabilities feeds Adapter.Capabilities() and the
-- Adaptive Router's Planner.
-- ---------------------------------------------------------------------------
CREATE TABLE providers (
    id                       text PRIMARY KEY,      -- slug
    display_name             text NOT NULL,
    category                 text,
    description              text,
    logo_url                 text,
    status                   text NOT NULL DEFAULT 'DEPRIORITIZED'
                             CHECK (status IN ('ACTIVE-CANDIDATE', 'DEPRIORITIZED', 'EXCLUDED')),
    compliance_review_status text,
    op_state                 text NOT NULL DEFAULT 'disabled'
                             CHECK (op_state IN ('enabled', 'disabled', 'paused', 'maintenance')),
    visibility               text NOT NULL DEFAULT 'tenant_readable',
    priority                 int,
    base_url                 text,
    api_version              text,
    auth_scheme              text CHECK (auth_scheme IN
                             ('api-key-header', 'api-key-query', 'bearer', 'basic', 'oauth2-cc')),
    auth_header              text,
    auth_query_param         text,
    capabilities             jsonb,                 -- [{field, cost_credits, expected_confidence}]
    region                   text[],
    docs_url                 text,
    webhook_config           jsonb,
    bulk_api                 boolean,
    batch_api                boolean,
    retry_policy             jsonb,
    timeout_ms               int,
    rate_limit_rpm           int,
    concurrency_limit        int,
    daily_limit              bigint,
    monthly_limit            bigint,
    breaker_threshold        int,
    breaker_cooldown_s       int,
    credit_sync              jsonb,                 -- {mode: header|endpoint|manual, endpoint, interval_s}
    credits_remaining        bigint,
    unit_cost_credits        bigint,
    cost_currency            text,
    sla_uptime_pct           real,
    correlation_group        text,
    sunset_at                timestamptz,
    confidence_score         real,
    cost_score               real,
    performance_score        real,
    success_score            real,
    failure_score            real,
    health_score             real,
    avg_latency_ms           real,
    last_health_at           timestamptz,
    last_failure_at          timestamptz,
    last_success_at          timestamptz,
    last_sync_at             timestamptz,
    tags                     text[],
    notes                    text,
    attrs                    jsonb,                 -- presentation-only; never planner-read
    archived_at              timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    updated_by               uuid
);

-- ---------------------------------------------------------------------------
-- secret_envelopes — AES-256-GCM envelope store (ADR-0017). Created before its consumers
-- so their FKs can reference it. Operator-only; ONLY internal/dash/secrets reads it.
-- ---------------------------------------------------------------------------
-- secret_envelopes + the users.mfa_totp_envelope_id FK were moved to migration 0004 by
-- Deviation D-1 (doc 12 P0): P0's MFA enroll/verify must seal/open totp_seed envelopes, so the
-- sealed store and its FK ship in 0004 alongside users. This migration assumes it already exists.

-- ---------------------------------------------------------------------------
-- key_import_batches — audited async bulk import provenance (25MB/50k caps enforced in code).
-- ---------------------------------------------------------------------------
CREATE TABLE key_import_batches (
    id          uuid PRIMARY KEY,
    provider_id text NOT NULL REFERENCES providers(id),
    source      text NOT NULL CHECK (source IN ('csv', 'xlsx', 'json', 'paste')),
    total       int  NOT NULL DEFAULT 0,
    succeeded   int  NOT NULL DEFAULT 0,
    failed      int  NOT NULL DEFAULT 0,
    errors      jsonb,
    status      text NOT NULL DEFAULT 'running',
    created_by  uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

-- ---------------------------------------------------------------------------
-- key_pools — Key Pool per Provider; selector = provider_id || ':' || name matches
-- provider.AuthDescriptor.KeyPoolSelector. owner_tenant_id NULL = platform-managed,
-- non-NULL = Tenant BYO (enumerated read-projection below).
-- ---------------------------------------------------------------------------
CREATE TABLE key_pools (
    id              uuid PRIMARY KEY,
    provider_id     text NOT NULL REFERENCES providers(id),
    name            text NOT NULL,
    strategy        text NOT NULL CHECK (strategy IN
                    ('round_robin', 'least_used', 'weighted', 'credit_based', 'region_based',
                     'lowest_latency', 'highest_success', 'ai_routing', 'random', 'priority',
                     'failover', 'overflow')),
    strategy_params jsonb,
    owner_tenant_id text,
    status          text NOT NULL DEFAULT 'active',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, name)
);

-- ---------------------------------------------------------------------------
-- provider_keys — Provider Key metadata + runtime counters. status is the KM-3 state machine
-- (spec §5): active/disabled/paused/exhausted/rate_limited/auth_failed/expired/rotating/
-- archived. The secret itself lives ONLY in secret_envelopes.
-- ---------------------------------------------------------------------------
CREATE TABLE provider_keys (
    id                   uuid PRIMARY KEY,
    provider_id          text NOT NULL REFERENCES providers(id),
    label                text,
    secret_envelope_id   uuid NOT NULL REFERENCES secret_envelopes(id),
    secret_last4         text,
    auth_method          text,
    status               text NOT NULL DEFAULT 'active' CHECK (status IN
                         ('active', 'disabled', 'paused', 'exhausted', 'rate_limited',
                          'auth_failed', 'expired', 'rotating', 'archived')),
    disable_reason       text,
    health               text,
    weight               int NOT NULL DEFAULT 100,
    priority             int,
    region               text,
    environment          text,
    team                 text,
    owner                text,
    notes                text,
    daily_limit          bigint,
    monthly_limit        bigint,
    rpm_limit            int,
    concurrency_limit    int,
    credits_remaining    bigint,
    credits_used         bigint,
    credits_synced_at    timestamptz,
    consecutive_failures int    NOT NULL DEFAULT 0,
    timeout_count        bigint NOT NULL DEFAULT 0,
    retry_count          bigint NOT NULL DEFAULT 0,
    error_counters       jsonb,
    latency_ewma_ms      real,
    success_ewma         real,
    cost_per_call        bigint,
    active_requests      int NOT NULL DEFAULT 0,
    last_used_at         timestamptz,
    last_success_at      timestamptz,
    last_failure_at      timestamptz,
    last_health_at       timestamptz,
    last_rotated_at      timestamptz,
    rotated_to           uuid REFERENCES provider_keys(id),
                         -- successor Provider Key created by POST /keys/{id}/rotate
                         -- (doc 04 §2.4): durable rotation lineage on the key row itself
    rotate_overlap_until timestamptz,
                         -- overlap deadline while status='rotating' (old + new both valid);
                         -- the rotation state machine auto-archives the old key
                         -- (rotating -> archived) when overlap ends (doc 07 §9) — durable
                         -- across instance restarts
    expires_at           timestamptz,
    owner_tenant_id      text,
    rotation_group       text,
    imported_batch_id    uuid REFERENCES key_import_batches(id),
    tags                 text[],
    created_by           uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX provider_keys_provider_status_idx ON provider_keys (provider_id, status);
CREATE INDEX provider_keys_active_idx ON provider_keys (provider_id) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- key_pool_members — Key Pool membership.
-- ---------------------------------------------------------------------------
CREATE TABLE key_pool_members (
    pool_id uuid NOT NULL REFERENCES key_pools(id),
    key_id  uuid NOT NULL REFERENCES provider_keys(id),
    PRIMARY KEY (pool_id, key_id)
);

-- ---------------------------------------------------------------------------
-- key_budgets — atomic lease counters per Provider Key (batched leases, §9.1). One row per
-- key; day/month windows roll over in place under the same row lock.
-- ---------------------------------------------------------------------------
CREATE TABLE key_budgets (
    key_id       uuid PRIMARY KEY REFERENCES provider_keys(id),
    day          date    NOT NULL,
    day_used     bigint  NOT NULL DEFAULT 0,
    day_leased   bigint  NOT NULL DEFAULT 0,
    month        char(7) NOT NULL,   -- 'YYYY-MM' (UTC)
    month_used   bigint  NOT NULL DEFAULT 0,
    month_leased bigint  NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- health_schedules — per-Provider health-check schedule backing GET/PUT /health/schedules
-- (doc 04 §2.6; editor in doc 09; scheduler contract in doc 10 §3.3, default 60s). Typed
-- columns, deliberately NOT providers.attrs: attrs is presentation-only and may never be
-- read by the scheduler. Owned by internal/dash/health (§6); missing row = defaults below.
-- ---------------------------------------------------------------------------
CREATE TABLE health_schedules (
    provider_id text PRIMARY KEY REFERENCES providers(id),
    interval_s  int  NOT NULL DEFAULT 60,
    jitter_pct  int  NOT NULL DEFAULT 10,
    regions     text[],
    enabled     boolean NOT NULL DEFAULT true,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid
);

-- ---------------------------------------------------------------------------
-- rotation_triggers — persisted trigger thresholds/cooldowns backing GET/PUT
-- /rotation/triggers (doc 04 §2.5; semantics in doc 07 §9). One row per trigger kind; the
-- kind vocabulary is CLOSED and validated in the service layer against the doc 07 §9
-- mapping table. Missing row = in-code default, but every PUT persists here — a PUT
-- endpoint may never write to memory only. Owned by internal/dash/rotation (§6).
--
-- DELIBERATELY live config, not a config_versions kind (OI-DB-8). Justification: this is a
-- handful of operator-only single-row knobs, not tenant-authored payloads — (a) the surface
-- is operator-only (Class P platform_only RLS + RBAC O on GET/PUT); (b) every PUT flows
-- through the audited() wrapper, so audit_log carries full before/after images of each row;
-- (c) the doc 04 §2.5 validator rejects unsafe configs (AUTH -> auth_failed handling can
-- never be disabled); (d) a bad change is reversible by re-PUTting the audit row's `before`
-- image (each row is one self-contained document — no partial state), and deleting a row
-- restores the in-code default. The configver lifecycle (draft/validate/publish, approval
-- pinning, epochs) exists for multi-object tenant payloads; grafting it here would add a
-- publish path without adding recoverability the audit before-image doesn't already provide.
-- Operational rollback procedure: GET /v1/admin/audit-log?object_kind=rotation_triggers,
-- take the last-known-good `before` image, re-PUT it (itself audited).
-- ---------------------------------------------------------------------------
CREATE TABLE rotation_triggers (
    trigger    text PRIMARY KEY,      -- trigger kind (closed vocabulary, doc 07 §9)
    thresholds jsonb,
    cooldown_s int,
    enabled    boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by uuid
);

-- ---------------------------------------------------------------------------
-- Class P RLS: platform-only for ALL commands, then the two enumerated tenant
-- read-projections. secret_envelopes gets NO tenant policy, ever.
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    -- secret_envelopes is omitted here: it and its platform-only policy moved to 0004 (Deviation D-1).
    FOREACH t IN ARRAY ARRAY['providers', 'key_import_batches',
                             'key_pools', 'provider_keys', 'key_pool_members', 'key_budgets',
                             'health_schedules', 'rotation_triggers'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_platform_only ON %1$I
                USING (app_current_tenant() = 'platform')
                WITH CHECK (app_current_tenant() = 'platform')
        $f$, t);
    END LOOP;
END $$;

-- Enumerated tenant read-projections (ADR-0020). Catalog fields only, via the view below;
-- BYO rows expose the owning Tenant's own keys/pools.
CREATE POLICY providers_tenant_catalog_read ON providers
    FOR SELECT USING (visibility = 'tenant_readable');
CREATE POLICY provider_keys_byo_read ON provider_keys
    FOR SELECT USING (owner_tenant_id = app_current_tenant());
CREATE POLICY key_pools_byo_read ON key_pools
    FOR SELECT USING (owner_tenant_id = app_current_tenant());

-- Tenant-facing catalog projection: catalog/presentation columns ONLY — no limits, breaker
-- tunables, credit balances, or scores. FORCE RLS on providers applies to the view's owner
-- as well, so this view cannot widen row access; it only narrows columns.
CREATE VIEW providers_catalog AS
    SELECT id, display_name, category, description, logo_url, status, capabilities,
           region, docs_url, tags, sunset_at, archived_at
    FROM providers
    WHERE visibility = 'tenant_readable';
