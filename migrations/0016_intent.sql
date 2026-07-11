-- Migration 0016 — computed intent: per-account class scores (ADR-0027,
-- docs/research-intelligence/05). internal/intent is the single writer; these rows back
-- GET /v1/intent/accounts/{domain}. The richer per-signal breakdown lives in `reasoning`; the
-- single-valued intent_score / intent_topics / buying_signal Fields are written back to
-- field_versions separately (intent is the sole write-back owner).
--
-- Class-T tenant isolation (gate G1), same mechanism as 0001/0015: the app connects with a role that
-- has NO BYPASSRLS and runs, per transaction, SET LOCAL app.current_tenant = '<tenant-from-principal>';
-- every policy scopes rows to that setting (app_current_tenant() helper defined in 0001). No
-- BEGIN/COMMIT here — internal/pgmigrate applies each file atomically.
--
-- NOTE: the raw time+tenant-partitioned intent_signals feed (retained losers) is a follow-on; this
-- migration lands the customer-visible computed scores that the intent API serves.

CREATE TABLE intent_scores (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      text        NOT NULL,
    account        text        NOT NULL,               -- company_domain / account key
    signal_class   text        NOT NULL,               -- one of the 10 intent classes
    score          double precision NOT NULL CHECK (score >= 0 AND score <= 1),
    confidence     double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    signal_count   int         NOT NULL DEFAULT 0,
    reasoning      jsonb       NOT NULL DEFAULT '[]',   -- ordered per-signal contributions (the "why")
    config_version text        NOT NULL DEFAULT '',     -- pinned weights version (reproducibility)
    computed_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, account, signal_class)           -- latest score per (account,class); upsert target
);
CREATE INDEX intent_scores_account_idx ON intent_scores (tenant_id, account);

-- FORCE RLS (applies policies even to the table owner); the app role has no BYPASSRLS, so a bug
-- cannot cross tenants (G1).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['intent_scores'] LOOP
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
--   SELECT count(*) FROM intent_scores;   -- MUST be 0
