-- Migration 0008 — worker registry, queue definitions, Tenant budgets, bulk jobs
-- (doc 03 §2.5, docs 04/06).
--
-- workers holds BOTH status (actual, heartbeat-reported every 10s) and desired_state
-- (intent, written by audited dashboard actions); workers converge on their next beat.
-- status='lost' is DERIVED by the worker-lost detector when last_heartbeat_at is older than
-- 3 heartbeat intervals — a crashed worker never reports its own death.
--
-- queue_defs carries the OI-QW-3 scale-intent columns: POST /workers/scale and
-- PUT /queues/{name}/workers persist desired_replicas here through the queues store;
-- actuation is deploy-tool territory (doc 06 §5).
--
-- bulk_jobs is the durable progress record behind every 202 {job_id} bulk operation
-- (doc 04 §1.7/§4): per-item results persist on the job row (errors capped at 1,000
-- entries in code); the partial unique index makes the one-in-flight-per-(tenant, kind,
-- scope fingerprint) guard (409 bulk_job_conflict, doc 04 §4.2) a database invariant.
-- A submitted row is admitted 'queued' and unclaimed (claimed_by/lease_expires_at NULL); a
-- dashboardd instance claims it via UPDATE ... WHERE status='queued' (or expired lease),
-- setting claimed_by + lease_expires_at, renews the lease while processing, and commits
-- per-row so work is resumable. The bulk-job janitor loop (advisory lock 'dash_bulk_janitor')
-- transitions expired-lease queued/running rows to partial/failed — or back to 'queued' for
-- the resumable kinds (import, replay, rolling_restart) — so a dead instance releases the
-- one-in-flight index instead of wedging it forever (§4).
--
-- budgets are ALERTING objects only (doc 10): enforcement authority is the engine's
-- G4 cost ceiling gate (cost_ledger Reserve/Release/Committed), never this table.
-- Applied atomically by internal/pgmigrate; no BEGIN/COMMIT here.

CREATE TABLE workers (
    id                text PRIMARY KEY,
    kind              text,
    region            text,
    queue             text,
    version           text,
    status            text NOT NULL DEFAULT 'starting' CHECK (status IN
                      ('starting', 'running', 'draining', 'paused', 'stopped', 'lost')),
    desired_state     text NOT NULL DEFAULT 'running' CHECK (desired_state IN
                      ('running', 'draining', 'paused', 'stopped')),
    started_at        timestamptz,
    last_heartbeat_at timestamptz,
    cpu_pct           real,
    mem_mb            real,
    jobs_active       int    NOT NULL DEFAULT 0,
    jobs_done         bigint NOT NULL DEFAULT 0,
    restarts          int    NOT NULL DEFAULT 0,
    attrs             jsonb
);
CREATE INDEX workers_heartbeat_idx ON workers (last_heartbeat_at);
CREATE INDEX workers_queue_idx ON workers (queue, status);

CREATE TABLE queue_defs (
    name                text PRIMARY KEY,
    kind                text,
    max_attempts        int,
    visibility_s        int,
    description         text,
    -- Scale intent (doc 06 §5, OI-QW-3): desired worker replica count per queue. Written by
    -- the workers feature's scale endpoints (POST /workers/scale, PUT /queues/{name}/workers)
    -- THROUGH the queues store (single writer, §6); actuation is deploy-tool territory.
    desired_replicas    int,
    replicas_updated_at timestamptz,
    replicas_updated_by uuid
);

-- budgets: moved to 0006 per Deviation D-2 (doc 12 P3). The CREATE + its Class-T RLS ship in
-- migration 0006 (§2.3) because P3's routing/Waterfall validator (VR-7) cross-checks
-- max_cost_credits against a real Tenant budget row. 0008 no longer creates it.

-- ---------------------------------------------------------------------------
-- bulk_jobs — durable progress record for every 202 {job_id} bulk operation (doc 04 §1.7/§4):
-- POST keys/bulk, queues/{name}/replay, providers/{id}/benchmark, workers/rolling-restart,
-- health/checks/run. Per-item results persist on the job (errors capped at 1,000 entries in
-- code; doc 06 §3.4); key imports keep their richer key_import_batches record and are read
-- via GET /key-imports/{job_id}. Platform-scoped operator jobs use tenant_id = 'platform'
-- (ADR-0020 sentinel). scope_fingerprint is the canonical hash of the operation's target set.
-- ---------------------------------------------------------------------------
CREATE TABLE bulk_jobs (
    id                   uuid PRIMARY KEY,
    tenant_id            text NOT NULL,
    kind                 text NOT NULL,
    scope_fingerprint    text NOT NULL,
    status               text NOT NULL DEFAULT 'queued' CHECK (status IN
                         ('queued', 'running', 'succeeded', 'partial', 'failed')),
    claimed_by           text,
                         -- executing dashboardd instance id; NULL until an instance claims the
                         -- queued row via UPDATE ... WHERE status='queued' (or expired lease),
                         -- re-set on any re-claim
    lease_expires_at     timestamptz,
                         -- liveness lease, NULL until claimed; renewed by the executor while it
                         -- processes; expired lease = owner died — the janitor (§4) transitions
                         -- the row out of 'queued'/'running', releasing the one-in-flight index
    attempts             int NOT NULL DEFAULT 0,
                         -- claim count, incremented on each (re-)claim; bounds re-claim retries
    total                int NOT NULL DEFAULT 0,
    succeeded            int NOT NULL DEFAULT 0,
    failed               int NOT NULL DEFAULT 0,
    matched_at_execution int,
    errors               jsonb,
    error_summary        jsonb,
    results              jsonb,
    created_by           uuid,
    created_at           timestamptz NOT NULL DEFAULT now(),
    started_at           timestamptz,
    finished_at          timestamptz
);
-- doc 04 §4.2: at most ONE in-flight bulk job per (tenant, kind, scope fingerprint) — a
-- duplicate submit conflicts and the handler returns 409 bulk_job_conflict.
CREATE UNIQUE INDEX bulk_jobs_one_in_flight_uq ON bulk_jobs (tenant_id, kind, scope_fingerprint)
    WHERE status IN ('queued', 'running');
CREATE INDEX bulk_jobs_tenant_created_idx ON bulk_jobs (tenant_id, created_at DESC);
-- Janitor sweep (advisory lock 'dash_bulk_janitor', §4): expired-lease in-flight rows.
-- Non-resumable kinds: some per-item progress recorded (succeeded + failed > 0) -> 'partial',
-- none -> 'failed'; error_summary records {"lease_expired": true, "claimed_by": ...}. Resumable
-- kinds (import, replay, rolling_restart) go back to 'queued' for another instance to re-claim
-- and resume from the last committed row (rows commit independently); rolling-restart wave
-- progress persists in results. Either transition releases the one-in-flight unique index.
CREATE INDEX bulk_jobs_lease_expiry_idx ON bulk_jobs (lease_expires_at)
    WHERE status IN ('queued', 'running');

-- Class P: workers + queue_defs (platform-only).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['workers', 'queue_defs'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_platform_only ON %1$I
                USING (app_current_tenant() = 'platform')
                WITH CHECK (app_current_tenant() = 'platform')
        $f$, t);
    END LOOP;
END $$;

-- Class T: bulk_jobs (0001-style isolation; not on the operator-read list — platform-scoped
-- bulk jobs are simply rows with tenant_id = 'platform'). budgets moved to 0006 (D-2).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['bulk_jobs'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;
