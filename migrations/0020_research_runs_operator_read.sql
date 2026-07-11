-- Migration 0020 — research_runs operator cross-Tenant read (ADR-0020 dual-GUC; docs/research-intelligence/08).
-- Completes the RBAC↔RLS contract for the Research Run monitor (GET /v1/admin/research/runs): the rbac
-- matrix grants the operator role DecisionAllow (cross-Tenant) for research.read, but 0015 shipped only the
-- Class-T research_runs_tenant_isolation policy — so under dual-GUC an operator (own tenant = the sentinel
-- 'platform') sees ZERO real-tenant runs: the matrix says "allow" while RLS silently returns nothing.
--
-- This is exactly the gap migration 0017 closed for research_dossiers + intent_scores; research_runs was
-- deferred when the run monitor landed. The fix is identical — an additive permissive FOR SELECT policy
-- scoped to the operator role. RLS OR-combines permissive policies, so tenant_admin/tenant_user stay
-- confined by *_tenant_isolation (own-tenant), while an operator additionally reads runs across tenants —
-- READ ONLY (no WITH CHECK; the run monitor never writes, and operators still cannot INSERT/UPDATE/DELETE
-- cross-tenant). The app role has no BYPASSRLS, so this enumerated grant is the ONLY cross-tenant path.
-- app_current_role() is defined by migration 0004; it precedes this file in the chain. No BEGIN/COMMIT —
-- internal/pgmigrate applies each file atomically.

CREATE POLICY research_runs_operator_read ON research_runs
    FOR SELECT USING (app_current_role() = 'operator');

-- Release-blocker (run as a NON-superuser against a real Postgres; superusers bypass RLS):
--   SET app.current_tenant='B'; SET app.current_role='tenant_user';
--   SELECT count(*) FROM research_runs WHERE tenant_id='A';   -- MUST be 0 (fail-closed)
--   SET app.current_tenant='platform'; SET app.current_role='operator';
--   SELECT count(*) FROM research_runs;                        -- MUST see A's + B's runs
