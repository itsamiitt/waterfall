-- Migration 0001 — core enrichment tables with tenant isolation (gate G1).
--
-- This is the canonical datastore-level enforcement that the in-memory store
-- (internal/store/memory.go) mirrors at the application layer. It realises the three
-- correctness-critical tables of docs/06 and diagrams/erd.mmd, with FORCE ROW LEVEL
-- SECURITY so an application bug cannot cross tenants (docs/04 §4, docs/18 §1).
--
-- The application connects with a role that has NO BYPASSRLS and, per transaction, runs:
--     SET LOCAL app.current_tenant = '<tenant-id-from-signed-principal>';
-- Every policy below scopes rows to that setting. tenant_id is NEVER taken from a
-- request body — only from the authenticated principal (internal/tenant).
--
-- Statements are not wrapped in BEGIN/COMMIT here: the migration runner
-- (internal/pgmigrate) applies each file inside a transaction together with its
-- schema_migrations record, so applying a file is atomic.

-- Helper: the current tenant for this transaction, from the session setting.
CREATE OR REPLACE FUNCTION app_current_tenant() RETURNS text
    LANGUAGE sql STABLE AS $$
    SELECT current_setting('app.current_tenant', /* missing_ok = */ true)
$$;

-- ---------------------------------------------------------------------------
-- field_versions — append-only provenance (G5). Winners AND losers retained
-- (W3C PROV / ADR-0006); the current best value is the highest confidence per field.
-- ---------------------------------------------------------------------------
CREATE TABLE field_versions (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       text        NOT NULL,
    subject_id      text        NOT NULL,
    field           text        NOT NULL,
    value           text        NOT NULL,
    confidence      double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    provider        text        NOT NULL,
    cost_credits    bigint      NOT NULL DEFAULT 0,
    obs_confidence  double precision NOT NULL,
    idempotency_key text        NOT NULL,               -- the G2 key that produced this value
    observed_at     timestamptz NOT NULL,
    -- G5 at the schema level: no bare, provenance-less value can be inserted.
    CONSTRAINT field_versions_provenance_not_bare
        CHECK (value <> '' AND provider <> '' AND idempotency_key <> '')
);
CREATE INDEX field_versions_current_idx
    ON field_versions (tenant_id, subject_id, field, confidence DESC);

-- ---------------------------------------------------------------------------
-- idempotency_ledger — exactly-once-effective provider calls (G2).
-- ---------------------------------------------------------------------------
CREATE TABLE idempotency_ledger (
    tenant_id       text        NOT NULL,
    idempotency_key text        NOT NULL,
    result          jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, idempotency_key)            -- first writer wins; replays short-circuit
);

-- ---------------------------------------------------------------------------
-- cost_ledger — per-job committed spend, the G4 ceiling accounting.
-- ---------------------------------------------------------------------------
CREATE TABLE cost_ledger (
    tenant_id       text        NOT NULL,
    job_id          text        NOT NULL,
    committed       bigint      NOT NULL DEFAULT 0 CHECK (committed >= 0),
    PRIMARY KEY (tenant_id, job_id)
);

-- ---------------------------------------------------------------------------
-- FORCE RLS on every table. FORCE also applies policies to the table owner, so even a
-- migration/owner connection cannot silently read across tenants.
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['field_versions', 'idempotency_ledger', 'cost_ledger'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Release-blocker test (docs/21 §1, run in CI against a real Postgres):
--   SET app.current_tenant = 'A'; INSERT ... ; SET app.current_tenant = 'B';
--   SELECT count(*) FROM field_versions;   -- MUST be 0
