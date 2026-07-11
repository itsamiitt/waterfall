-- Migration 0019 — CRM outbound (roadmap, ADR-0030; docs/research-intelligence/15 §4.3). internal/crm is
-- the single owner of these tables. This lands the connection/field-map config + the idempotent push
-- ledger + their tenant-isolation contract; the push itself is a CRM connector adapter executed THROUGH
-- the single egress-proxy (ADR-0030/0010) — no second internet route — and attaches later.
--
-- Key custody (ADR-0017): a CRM OAuth secret is NEVER stored here in plaintext. crm_connections.secret_ref
-- holds only a reference (envelope id) into the sealed secrets backend; the token is injected at the
-- egress boundary, never in the control-plane.
--
-- G2 idempotency (ADR-0030): crm_push_ledger.idem_key = hash(tenant, connection, record,
-- field_map_version, dossier_version); UNIQUE (tenant_id, idem_key) makes a retry/redelivery a no-op so a
-- push never double-writes the CRM. DSAR (09 §5): a ledger row records the downstream erasure obligation so
-- an erasure cascade propagates to what was pushed.
--
-- Class-T tenant isolation (gate G1), same mechanism as 0001/0015/0016/0018: the app connects with a role
-- that has NO BYPASSRLS and runs, per transaction, SET LOCAL app.current_tenant = '<tenant>'; every policy
-- scopes rows to that setting. tenant_id is NEVER taken from a request body. No BEGIN/COMMIT here —
-- internal/pgmigrate applies each file atomically.

-- ---------------------------------------------------------------------------
-- crm_connections — one configured CRM connection for a Tenant. secret_ref is an envelope reference only.
-- ---------------------------------------------------------------------------
CREATE TABLE crm_connections (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     text        NOT NULL,
    connection_id text        NOT NULL,               -- stable id used by field maps + the push ledger
    provider      text        NOT NULL,               -- CRM provider slug (e.g. 'salesforce', 'hubspot')
    status        text        NOT NULL DEFAULT 'active',
    secret_ref    text        NOT NULL DEFAULT '',    -- ADR-0017 envelope id (NO plaintext token)
    config        jsonb       NOT NULL DEFAULT '{}',  -- instance URL, object types, etc.
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, connection_id)
);

-- ---------------------------------------------------------------------------
-- crm_field_maps — a versioned Dossier-field → CRM-field mapping for a connection. The version is part of
-- the push idempotency key, so remapping produces distinct pushes.
-- ---------------------------------------------------------------------------
CREATE TABLE crm_field_maps (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     text        NOT NULL,
    connection_id text        NOT NULL,
    version       int         NOT NULL,
    mapping       jsonb       NOT NULL DEFAULT '{}',  -- {dossier_field: crm_field}
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, connection_id, version)
);

-- ---------------------------------------------------------------------------
-- crm_push_ledger — the idempotent record of what was pushed to a CRM (G2 + DSAR provenance). A push with
-- an already-present idem_key is a no-op. status tracks the DSAR erasure obligation.
-- ---------------------------------------------------------------------------
CREATE TABLE crm_push_ledger (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id         text        NOT NULL,
    connection_id     text        NOT NULL,
    idem_key          text        NOT NULL,           -- hash(tenant,connection,record,field_map_version,dossier_version)
    record            text        NOT NULL,           -- the pushed record key (account / contact)
    field_map_version int         NOT NULL DEFAULT 0,
    dossier_version   text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'pushed'
                      CHECK (status IN ('pushed', 'failed', 'erasure_pending', 'erased')),
    pushed_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, idem_key)                       -- G2: a redelivered push is a no-op
);
CREATE INDEX crm_push_ledger_record_idx ON crm_push_ledger (tenant_id, record);

-- ---------------------------------------------------------------------------
-- FORCE RLS on every table (applies policies even to the table owner). The app role has no BYPASSRLS, so
-- cross-tenant reads/pushes are impossible even with an application bug (G1).
-- ---------------------------------------------------------------------------
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['crm_connections', 'crm_field_maps', 'crm_push_ledger'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %1$s_tenant_isolation ON %1$I
                USING (tenant_id = app_current_tenant())
                WITH CHECK (tenant_id = app_current_tenant())
        $f$, t);
    END LOOP;
END $$;

-- Release-blocker (run as a NON-superuser against a real Postgres; superusers bypass RLS):
--   SET app.current_tenant='A'; INSERT ...; SET app.current_tenant='B';
--   SELECT count(*) FROM crm_connections;  -- MUST be 0
--   SELECT count(*) FROM crm_push_ledger;  -- MUST be 0  (Tenant A cannot push into Tenant B's connection)
