-- Migration 0015 — research: Runs, Steps, Dossiers, and Source provenance (ADR-0028,
-- docs/research-intelligence/06). internal/research is the single owner of these tables; they back
-- the async POST /v1/research (202+job_id) flow, GET /v1/research/{id}, GET /v1/dossiers/{domain},
-- and the queryable per-value provenance (G5).
--
-- Class-T tenant isolation (gate G1), same mechanism as 0001: the app connects with a role that has
-- NO BYPASSRLS and runs, per transaction, SET LOCAL app.current_tenant = '<tenant-from-principal>';
-- every policy scopes rows to that setting (app_current_tenant() helper defined in 0001). tenant_id
-- is NEVER taken from a request body. No BEGIN/COMMIT here — internal/pgmigrate applies each file
-- inside a transaction with its schema_migrations record, so applying a file is atomic.

-- ---------------------------------------------------------------------------
-- research_runs — one Research Run (a domain→Dossier assembly) with its lifecycle status.
-- ---------------------------------------------------------------------------
CREATE TABLE research_runs (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      text        NOT NULL,
    run_id         text        NOT NULL,
    subject_key    text        NOT NULL,               -- resolved subject (usually company_domain)
    status         text        NOT NULL DEFAULT 'queued',
    config_version text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, run_id)                          -- idempotent submission target
);
CREATE INDEX research_runs_subject_idx ON research_runs (tenant_id, subject_key);

-- ---------------------------------------------------------------------------
-- research_steps — one row per Agent Task in a Run: model/prompt/tokens/cost/outcome (accounting
-- + audit; the retained losers of the cascade are recorded here).
-- ---------------------------------------------------------------------------
CREATE TABLE research_steps (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      text        NOT NULL,
    run_id         text        NOT NULL,
    task_type      text        NOT NULL,
    model_slug     text        NOT NULL DEFAULT '',
    prompt_version text        NOT NULL DEFAULT '',
    tokens_in      int         NOT NULL DEFAULT 0,
    tokens_out     int         NOT NULL DEFAULT 0,
    cost_credits   bigint      NOT NULL DEFAULT 0,
    latency_ms     int         NOT NULL DEFAULT 0,
    outcome        text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX research_steps_run_idx ON research_steps (tenant_id, run_id);

-- ---------------------------------------------------------------------------
-- research_dossiers — the assembled Dossier JSON, latest per (tenant, dossier_id); freshness drives
-- background refresh. subject_key is indexed for GET /v1/dossiers/{domain}.
-- ---------------------------------------------------------------------------
CREATE TABLE research_dossiers (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          text        NOT NULL,
    dossier_id         text        NOT NULL,
    subject_key        text        NOT NULL,
    dossier            jsonb       NOT NULL,
    overall_confidence double precision NOT NULL DEFAULT 0 CHECK (overall_confidence >= 0 AND overall_confidence <= 1),
    config_version     text        NOT NULL DEFAULT '',
    freshness_at       timestamptz NOT NULL DEFAULT now(),
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, dossier_id)
);
CREATE INDEX research_dossiers_subject_idx ON research_dossiers (tenant_id, subject_key, freshness_at DESC);

-- ---------------------------------------------------------------------------
-- research_sources — queryable per-value provenance (G5): which provider produced each Dossier
-- value, of what Source Type (api | dataset | ai_inference), at what cost. AI-inferred values are
-- distinguished and never fused as high-confidence facts.
-- ---------------------------------------------------------------------------
CREATE TABLE research_sources (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    text        NOT NULL,
    dossier_id   text        NOT NULL,
    field        text        NOT NULL,
    provider     text        NOT NULL,
    source_type  text        NOT NULL CHECK (source_type IN ('api', 'dataset', 'ai_inference')),
    cost_credits bigint      NOT NULL DEFAULT 0,
    idem_key     text        NOT NULL DEFAULT '',
    confidence   double precision NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX research_sources_dossier_idx ON research_sources (tenant_id, dossier_id);

-- ---------------------------------------------------------------------------
-- FORCE RLS on every table (applies policies even to the table owner). The app role has no
-- BYPASSRLS, so cross-tenant reads are impossible even with an application bug (G1).
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['research_runs', 'research_steps', 'research_dossiers', 'research_sources'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Release-blocker (docs/21 §1, run as a NON-superuser against a real Postgres):
--   SET app.current_tenant='A'; INSERT ...; SET app.current_tenant='B';
--   SELECT count(*) FROM research_dossiers;   -- MUST be 0
