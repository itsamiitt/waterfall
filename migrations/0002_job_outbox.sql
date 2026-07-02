-- Migration 0002 — transactional-outbox durable job queue (docs/10 §4, docs/35).
--
-- A submitted job is durably captured as a row here (status='queued', pending=true) BEFORE
-- any async processing. A relay claims pending rows with FOR UPDATE SKIP LOCKED (the
-- competing-consumers pattern — multiple relay replicas poll without double-claiming) and
-- feeds them to the worker pool. `pending` is the outbox intent: it is cleared ONLY when the
-- job reaches a durable terminal state (in the same UPDATE as the terminal snapshot), so a
-- crash before that leaves the row pending and the relay re-drives it after the visibility
-- timeout (at-least-once). The engine's G2 idempotency makes redelivery free of double effect.
--
-- Requires migration 0001 (app_current_tenant()). Applied atomically by the migration
-- runner (internal/pgmigrate); no explicit BEGIN/COMMIT here.

CREATE TABLE job_outbox (
    job_id      text        PRIMARY KEY,
    tenant_id   text        NOT NULL,
    payload     jsonb       NOT NULL,           -- serialized job.Job (carries the principal, G1)
    status      text        NOT NULL,           -- queued | running | succeeded | failed
    pending     boolean     NOT NULL DEFAULT true, -- outbox intent: true until durably terminal
    claimed_at  timestamptz,                    -- last relay claim (drives the visibility timeout)
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- The relay scans only pending rows, oldest first.
CREATE INDEX job_outbox_pending_idx ON job_outbox (claimed_at, created_at) WHERE pending;

-- Tenant isolation for TENANT access (a tenant reading its own job status). The relay is a
-- trusted system consumer that runs as a BYPASSRLS role to claim across tenants; each row
-- still carries tenant_id, which flows into execution via the job's captured principal (G1).
ALTER TABLE job_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY job_outbox_tenant_isolation ON job_outbox
    USING (tenant_id = app_current_tenant())
    WITH CHECK (tenant_id = app_current_tenant());
