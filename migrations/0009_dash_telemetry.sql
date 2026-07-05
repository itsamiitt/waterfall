-- Migration 0009 — telemetry: raw usage_events feed + all rollup families (doc 03 §2.6,
-- doc 10; resolves RF-2 via provider_health_1d; RF-3 boundary documented in doc 03).
--
-- Hot-path contract: the engine writes exactly ONE row per Provider call into usage_events
-- (attributed to tenant/provider/key/workflow/country via rotation.LeaseResolver Done);
-- everything else is folded by the leader aggregator (advisory lock 'dash_aggregator').
-- Incremental folds are additive upserts: INSERT ... ON CONFLICT (dims, bucket) DO UPDATE
-- SET x = x + EXCLUDED.x; repair refolds recompute whole buckets and REPLACE (doc 03 §9.4).
-- lat_hist holds 20 fixed log-spaced buckets (code-enforced; Postgres does not enforce
-- array bounds); percentiles are computed at read time.
--
-- All tables here are RANGE-partitioned (1m tables weekly, raw feeds daily, others
-- monthly); partitions are created/dropped by the runtime partition maintainer (doc 03 §4),
-- whose ensure pass runs synchronously at dashboardd startup. Migrations create parents and
-- DEFAULT backstop partitions only. Applied atomically by internal/pgmigrate; no
-- BEGIN/COMMIT here.

-- ---------------------------------------------------------------------------
-- usage_events — append-only raw feed; daily partitions; 48h retention; refold source.
-- outcome_class: 'ok' or one of the 8 error classes AUTH, RATE_LIMIT, TRANSIENT, NOT_FOUND,
-- BAD_REQUEST, QUOTA, PROVIDER_DOWN, UNKNOWN (domain.ErrorClass).
-- ---------------------------------------------------------------------------
CREATE TABLE usage_events (
    tenant_id     text NOT NULL,
    provider_id   text NOT NULL,
    key_id        uuid,
    workflow_key  text,
    country       text,
    outcome_class text NOT NULL,
    credits       bigint NOT NULL DEFAULT 0,
    lat_ms        int,
    created_at    timestamptz NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);
CREATE TABLE usage_events_default PARTITION OF usage_events DEFAULT;
CREATE INDEX usage_events_created_idx ON usage_events (created_at);

-- ---------------------------------------------------------------------------
-- provider_stats_{1m,1h,1d} — per-Provider outcome/latency/credit rollups; failure columns
-- mirror the 8-class error taxonomy 1:1. Retention 7d / 90d / 2y.
-- ---------------------------------------------------------------------------
CREATE TABLE provider_stats_1m (
    provider_id        text NOT NULL,
    bucket_start       timestamptz NOT NULL,
    req                bigint NOT NULL DEFAULT 0,
    ok                 bigint NOT NULL DEFAULT 0,
    fail_auth          bigint NOT NULL DEFAULT 0,
    fail_rate_limit    bigint NOT NULL DEFAULT 0,
    fail_transient     bigint NOT NULL DEFAULT 0,
    fail_not_found     bigint NOT NULL DEFAULT 0,
    fail_bad_request   bigint NOT NULL DEFAULT 0,
    fail_quota         bigint NOT NULL DEFAULT 0,
    fail_provider_down bigint NOT NULL DEFAULT 0,
    fail_unknown       bigint NOT NULL DEFAULT 0,
    timeout_count      bigint NOT NULL DEFAULT 0,
    credits_spent      bigint NOT NULL DEFAULT 0,
    lat_sum_ms         bigint NOT NULL DEFAULT 0,
    lat_hist           bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (provider_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE provider_stats_1m_default PARTITION OF provider_stats_1m DEFAULT;
CREATE INDEX provider_stats_1m_bucket_idx ON provider_stats_1m (bucket_start);

CREATE TABLE provider_stats_1h (
    provider_id        text NOT NULL,
    bucket_start       timestamptz NOT NULL,
    req                bigint NOT NULL DEFAULT 0,
    ok                 bigint NOT NULL DEFAULT 0,
    fail_auth          bigint NOT NULL DEFAULT 0,
    fail_rate_limit    bigint NOT NULL DEFAULT 0,
    fail_transient     bigint NOT NULL DEFAULT 0,
    fail_not_found     bigint NOT NULL DEFAULT 0,
    fail_bad_request   bigint NOT NULL DEFAULT 0,
    fail_quota         bigint NOT NULL DEFAULT 0,
    fail_provider_down bigint NOT NULL DEFAULT 0,
    fail_unknown       bigint NOT NULL DEFAULT 0,
    timeout_count      bigint NOT NULL DEFAULT 0,
    credits_spent      bigint NOT NULL DEFAULT 0,
    lat_sum_ms         bigint NOT NULL DEFAULT 0,
    lat_hist           bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (provider_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE provider_stats_1h_default PARTITION OF provider_stats_1h DEFAULT;

CREATE TABLE provider_stats_1d (
    provider_id        text NOT NULL,
    bucket_start       timestamptz NOT NULL,
    req                bigint NOT NULL DEFAULT 0,
    ok                 bigint NOT NULL DEFAULT 0,
    fail_auth          bigint NOT NULL DEFAULT 0,
    fail_rate_limit    bigint NOT NULL DEFAULT 0,
    fail_transient     bigint NOT NULL DEFAULT 0,
    fail_not_found     bigint NOT NULL DEFAULT 0,
    fail_bad_request   bigint NOT NULL DEFAULT 0,
    fail_quota         bigint NOT NULL DEFAULT 0,
    fail_provider_down bigint NOT NULL DEFAULT 0,
    fail_unknown       bigint NOT NULL DEFAULT 0,
    timeout_count      bigint NOT NULL DEFAULT 0,
    credits_spent      bigint NOT NULL DEFAULT 0,
    lat_sum_ms         bigint NOT NULL DEFAULT 0,
    lat_hist           bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (provider_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE provider_stats_1d_default PARTITION OF provider_stats_1d DEFAULT;

-- ---------------------------------------------------------------------------
-- key_usage_{1m,1h,1d} — per-Provider-Key attribution (KM-3 triggers, per-key cost).
-- Retention 3d / 30d / 1y.
-- ---------------------------------------------------------------------------
CREATE TABLE key_usage_1m (
    key_id        uuid NOT NULL,
    bucket_start  timestamptz NOT NULL,
    req           bigint NOT NULL DEFAULT 0,
    ok            bigint NOT NULL DEFAULT 0,
    fail          bigint NOT NULL DEFAULT 0,
    credits_spent bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (key_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE key_usage_1m_default PARTITION OF key_usage_1m DEFAULT;

CREATE TABLE key_usage_1h (
    key_id        uuid NOT NULL,
    bucket_start  timestamptz NOT NULL,
    req           bigint NOT NULL DEFAULT 0,
    ok            bigint NOT NULL DEFAULT 0,
    fail          bigint NOT NULL DEFAULT 0,
    credits_spent bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (key_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE key_usage_1h_default PARTITION OF key_usage_1h DEFAULT;

CREATE TABLE key_usage_1d (
    key_id        uuid NOT NULL,
    bucket_start  timestamptz NOT NULL,
    req           bigint NOT NULL DEFAULT 0,
    ok            bigint NOT NULL DEFAULT 0,
    fail          bigint NOT NULL DEFAULT 0,
    credits_spent bigint NOT NULL DEFAULT 0,
    lat_hist      bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (key_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE key_usage_1d_default PARTITION OF key_usage_1d DEFAULT;

-- ---------------------------------------------------------------------------
-- tenant_usage_{1h,1d} — Class T rollups (Tenant-visible usage). Retention 90d / 2y.
-- PK dimensions are NOT NULL with '' defaults so the composite PK is total.
-- ---------------------------------------------------------------------------
CREATE TABLE tenant_usage_1h (
    tenant_id     text NOT NULL,
    provider_id   text NOT NULL,
    workflow_key  text NOT NULL DEFAULT '',
    bucket_start  timestamptz NOT NULL,
    req           bigint NOT NULL DEFAULT 0,
    fields_filled bigint NOT NULL DEFAULT 0,
    credits       bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, provider_id, workflow_key, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE tenant_usage_1h_default PARTITION OF tenant_usage_1h DEFAULT;

CREATE TABLE tenant_usage_1d (
    tenant_id     text NOT NULL,
    provider_id   text NOT NULL,
    workflow_key  text NOT NULL DEFAULT '',
    bucket_start  timestamptz NOT NULL,
    req           bigint NOT NULL DEFAULT 0,
    fields_filled bigint NOT NULL DEFAULT 0,
    credits       bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, provider_id, workflow_key, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE tenant_usage_1d_default PARTITION OF tenant_usage_1d DEFAULT;

-- ---------------------------------------------------------------------------
-- cost_rollup_1d — Class T canonical cost rollup. Dimensions (tenant, provider, workflow,
-- country, day) — deliberately NO key_id (RF-3 boundary, doc 03 §2.6). Retention 2y.
-- ---------------------------------------------------------------------------
CREATE TABLE cost_rollup_1d (
    tenant_id          text NOT NULL,
    provider_id        text NOT NULL,
    workflow_key       text NOT NULL DEFAULT '',
    country            text NOT NULL DEFAULT '',
    day                date NOT NULL,
    credits            bigint NOT NULL DEFAULT 0,
    calls              bigint NOT NULL DEFAULT 0,
    successful_results bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, provider_id, workflow_key, country, day)
) PARTITION BY RANGE (day);
CREATE TABLE cost_rollup_1d_default PARTITION OF cost_rollup_1d DEFAULT;

-- ---------------------------------------------------------------------------
-- queue_stats_{1m,1h} — queue state-vector rollups computed by the aggregator from
-- job_outbox scans (granted SELECT; doc 03 §6). Retention 7d / 30d.
-- ---------------------------------------------------------------------------
CREATE TABLE queue_stats_1m (
    queue        text NOT NULL,
    bucket_start timestamptz NOT NULL,
    depth        bigint NOT NULL DEFAULT 0,
    running      bigint NOT NULL DEFAULT 0,
    scheduled    bigint NOT NULL DEFAULT 0,
    delayed      bigint NOT NULL DEFAULT 0,
    retry        bigint NOT NULL DEFAULT 0,
    failed       bigint NOT NULL DEFAULT 0,
    dead         bigint NOT NULL DEFAULT 0,
    enq          bigint NOT NULL DEFAULT 0,
    deq          bigint NOT NULL DEFAULT 0,
    oldest_age_s int    NOT NULL DEFAULT 0,
    PRIMARY KEY (queue, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE queue_stats_1m_default PARTITION OF queue_stats_1m DEFAULT;
CREATE INDEX queue_stats_1m_bucket_idx ON queue_stats_1m (bucket_start);

CREATE TABLE queue_stats_1h (
    queue        text NOT NULL,
    bucket_start timestamptz NOT NULL,
    depth        bigint NOT NULL DEFAULT 0,
    running      bigint NOT NULL DEFAULT 0,
    scheduled    bigint NOT NULL DEFAULT 0,
    delayed      bigint NOT NULL DEFAULT 0,
    retry        bigint NOT NULL DEFAULT 0,
    failed       bigint NOT NULL DEFAULT 0,
    dead         bigint NOT NULL DEFAULT 0,
    enq          bigint NOT NULL DEFAULT 0,
    deq          bigint NOT NULL DEFAULT 0,
    oldest_age_s int    NOT NULL DEFAULT 0,
    PRIMARY KEY (queue, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE queue_stats_1h_default PARTITION OF queue_stats_1h DEFAULT;

-- ---------------------------------------------------------------------------
-- worker_heartbeats (raw 10s beats, 24h) + worker_stats_5m (fold, 30d). Sums and a
-- GREATEST-merged max keep the fold upsertable; averages computed at read (sum / beats).
-- ---------------------------------------------------------------------------
CREATE TABLE worker_heartbeats (
    worker_id   text NOT NULL,
    beat_at     timestamptz NOT NULL,
    status      text,
    cpu_pct     real,
    mem_mb      real,
    jobs_active int,
    jobs_done   bigint,
    PRIMARY KEY (worker_id, beat_at)
) PARTITION BY RANGE (beat_at);
CREATE TABLE worker_heartbeats_default PARTITION OF worker_heartbeats DEFAULT;

CREATE TABLE worker_stats_5m (
    worker_id       text NOT NULL,
    bucket_start    timestamptz NOT NULL,
    beats           int    NOT NULL DEFAULT 0,
    cpu_pct_sum     real   NOT NULL DEFAULT 0,
    mem_mb_sum      real   NOT NULL DEFAULT 0,
    jobs_active_max int    NOT NULL DEFAULT 0,
    jobs_done_delta bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (worker_id, bucket_start)
) PARTITION BY RANGE (bucket_start);
CREATE TABLE worker_stats_5m_default PARTITION OF worker_stats_5m DEFAULT;

-- ---------------------------------------------------------------------------
-- provider_health_checks (raw scheduled checks, 30d) + provider_health_1d (daily fold, 2y).
-- RF-2: the daily fold ships from day one — raw retention can never backfill 90-day bars.
-- ---------------------------------------------------------------------------
CREATE TABLE provider_health_checks (
    provider_id text NOT NULL,
    key_id      uuid,
    region      text,
    checked_at  timestamptz NOT NULL DEFAULT now(),
    status      text NOT NULL,          -- up | degraded | down
    http_status int,
    lat_ms      int,
    error_class text                    -- 8-class taxonomy or NULL
) PARTITION BY RANGE (checked_at);
CREATE TABLE provider_health_checks_default PARTITION OF provider_health_checks DEFAULT;
CREATE INDEX provider_health_checks_idx ON provider_health_checks (provider_id, checked_at DESC);

CREATE TABLE provider_health_1d (
    provider_id       text NOT NULL,
    day               date NOT NULL,
    checks            bigint NOT NULL DEFAULT 0,
    ok                bigint NOT NULL DEFAULT 0,
    degraded          bigint NOT NULL DEFAULT 0,
    down              bigint NOT NULL DEFAULT 0,
    maintenance_s     int    NOT NULL DEFAULT 0,
    lat_sum_ms        bigint NOT NULL DEFAULT 0,
    worst_error_class text,
    PRIMARY KEY (provider_id, day)
) PARTITION BY RANGE (day);
CREATE TABLE provider_health_1d_default PARTITION OF provider_health_1d DEFAULT;

-- ---------------------------------------------------------------------------
-- RLS. Class P (platform-only): all Provider/key/queue/worker telemetry. Class T
-- (0001-style + enumerated operator read): usage_events, tenant_usage_*, cost_rollup_1d.
-- usage_events gets NO operator policy — the aggregator folds per Tenant through the
-- dual-GUC tx helper (doc 03 §9.4, OI-DB-3).
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['provider_stats_1m', 'provider_stats_1h', 'provider_stats_1d',
                             'key_usage_1m', 'key_usage_1h', 'key_usage_1d',
                             'queue_stats_1m', 'queue_stats_1h',
                             'worker_heartbeats', 'worker_stats_5m',
                             'provider_health_checks', 'provider_health_1d'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_platform_only ON %1$I
                USING (app_current_tenant() = 'platform')
                WITH CHECK (app_current_tenant() = 'platform')
        $f$, t);
    END LOOP;
END $$;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['usage_events', 'tenant_usage_1h', 'tenant_usage_1d',
                             'cost_rollup_1d'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Operator cross-tenant READ (enumerated, ADR-0020): platform-wide cost/usage views.
CREATE POLICY tenant_usage_1h_operator_read ON tenant_usage_1h
    FOR SELECT USING (app_current_role() = 'operator');
CREATE POLICY tenant_usage_1d_operator_read ON tenant_usage_1d
    FOR SELECT USING (app_current_role() = 'operator');
CREATE POLICY cost_rollup_1d_operator_read ON cost_rollup_1d
    FOR SELECT USING (app_current_role() = 'operator');
