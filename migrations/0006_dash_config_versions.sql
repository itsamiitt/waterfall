-- Migration 0006 — config versioning: routing policies, Waterfall workflows, alert rulesets
-- (doc 03 §2.3, doc 07). One lifecycle, one publish path, one audit story.
--
-- config_versions rows are immutable once published/archived; drafts are mutable and any
-- edit after validate reverts status to 'draft' (payload_hash is pinned at validate — the
-- approval-pinning property, doc 05). config_active is the pointer table: publish = single
-- UPDATE in one tx (re-check status='validated' + payload_hash, flip pointer, bump
-- config_epochs, append audit row, NOTIFY). Enrichment Jobs pin config_version_id at start
-- (G5 provenance). Applied atomically by internal/pgmigrate; no BEGIN/COMMIT here.
--
-- Deviation D-2 (doc 12 P3): the `budgets` table CREATE + its Class-T RLS are pulled forward
-- from migration 0008 into 0006 because P3's Waterfall/routing validator (VR-7:
-- max_cost_credits must not exceed the Tenant budget) needs the table against real schema.
-- The DDL is copied verbatim from doc 03's 0008 block; 0008 leaves a "moved to 0006 per D-2"
-- note in its place. budgets are ALERTING objects only (doc 10) — enforcement authority is
-- the engine's G4 cost ceiling gate (cost_ledger Reserve/Release/Committed), never this table.

CREATE TABLE config_versions (
    id                uuid PRIMARY KEY,
    tenant_id         text NOT NULL,
    kind              text NOT NULL CHECK (kind IN
                      ('routing_policy', 'waterfall_workflow', 'alert_ruleset')),
    scope_key         text NOT NULL,
    version           int  NOT NULL,
    status            text NOT NULL DEFAULT 'draft' CHECK (status IN
                      ('draft', 'validated', 'published', 'archived')),
    payload           jsonb NOT NULL,
    payload_hash      bytea,              -- pinned at validate; re-checked at publish
    validation_report jsonb,
    parent_version_id uuid REFERENCES config_versions(id),
    created_by        uuid,
    created_at        timestamptz NOT NULL DEFAULT now(),
    published_at      timestamptz,
    published_by      uuid,
    UNIQUE (tenant_id, kind, scope_key, version)
);

CREATE TABLE config_active (
    tenant_id         text NOT NULL,
    kind              text NOT NULL,
    scope_key         text NOT NULL,
    active_version_id uuid NOT NULL REFERENCES config_versions(id),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, kind, scope_key)
);

-- Readers cache resolved config by epoch; publish bumps the epoch (per-instance PoolState
-- and planner caches rebuild on epoch change, cross-instance convergence <= 1s [UNVERIFIED]).
-- The kind vocabulary is CLOSED and pinned here as a CHECK so the bump sites and the rebuild
-- watchers can never drift on the literal (doc 13's epoch-propagation test asserts the exact
-- strings): the three config_versions kinds plus the two SENTINEL kinds bumped outside the
-- publish path — ('platform','provider_catalog') for providers CRUD/op-state changes and
-- ('platform','key_pool') (SINGULAR) for key-pool strategy/membership and provider-key
-- rotation/compromise (doc 02 §2.2/§5, doc 07 §8.1/§10). All bumps, sentinel or not, execute
-- through the configver-owned BumpEpoch API (§6).
CREATE TABLE config_epochs (
    tenant_id text   NOT NULL,
    kind      text   NOT NULL CHECK (kind IN
              ('routing_policy', 'waterfall_workflow', 'alert_ruleset',
               'provider_catalog', 'key_pool')),
    epoch     bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, kind)
);

-- Denormalized Waterfall workflow list view (maintained by configver in the publish tx).
CREATE TABLE workflow_index (
    tenant_id  text NOT NULL,
    scope_key  text NOT NULL,
    name       text NOT NULL,
    trigger    text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, scope_key)
);

-- ---------------------------------------------------------------------------
-- budgets — Deviation D-2 (doc 12 P3): moved here from 0008 so P3's VR-7 validator can
-- cross-check max_cost_credits against a real Tenant budget row. Class T (0001-style
-- isolation); doctrine: budgets alert, G4 Cost Ceilings enforce — this table never gates
-- execution. Owned by internal/dash/cost once it lands (P6); read-only here in P3.
-- ---------------------------------------------------------------------------
CREATE TABLE budgets (
    tenant_id     text NOT NULL,
    scope         text NOT NULL CHECK (scope IN ('tenant', 'provider', 'workflow')),
    scope_key     text NOT NULL DEFAULT '',
    period        text NOT NULL CHECK (period IN ('day', 'month')),
    limit_credits bigint NOT NULL,
    alert_pct     int[],
    PRIMARY KEY (tenant_id, scope, scope_key, period)
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['config_versions', 'config_active', 'config_epochs',
                             'workflow_index', 'budgets'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Operator cross-tenant READ (enumerated, ADR-0020): config review + the sunset sweep
-- (every serving handler writes an audit_log row). budgets is NOT on the operator-read list.
CREATE POLICY config_versions_operator_read ON config_versions
    FOR SELECT USING (app_current_role() = 'operator');
CREATE POLICY config_active_operator_read ON config_active
    FOR SELECT USING (app_current_role() = 'operator');
CREATE POLICY workflow_index_operator_read ON workflow_index
    FOR SELECT USING (app_current_role() = 'operator');
