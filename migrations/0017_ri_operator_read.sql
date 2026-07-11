-- Migration 0017 — R&I operator cross-tenant READ (ADR-0020 dual-GUC, docs/research-intelligence/08).
-- Completes the RBAC↔RLS contract for the Slice 26 dashboards: the rbac matrix grants the operator
-- role DecisionAllow (cross-tenant) for research.read / intent.read, but 0015/0016 shipped only the
-- Class-T *_tenant_isolation policies (USING tenant_id = app_current_tenant()). Under dual-GUC the
-- operator's own tenant is the sentinel 'platform', so without an additive operator policy an operator
-- sees ZERO real-tenant rows — the matrix says "allow" while RLS silently returns nothing.
--
-- Fix, identical in shape to 0009 (tenant_usage_*/cost_rollup_1d), 0006 (config_versions), and 0007
-- (alerts): an *additive* permissive FOR SELECT policy scoped to the operator role. RLS combines
-- multiple permissive policies with OR, so tenant_admin/tenant_user remain confined by
-- *_tenant_isolation (own-tenant) while an operator additionally reads across tenants — READ ONLY
-- (no WITH CHECK; the R&I read-models never write, and operators still cannot INSERT/UPDATE/DELETE
-- cross-tenant). The app role has no BYPASSRLS, so this enumerated grant is the ONLY cross-tenant path.
--
-- Scope = exactly the two tables the Slice 26 read-models query (research_dossiers via
-- internal/dash/research, intent_scores via internal/dash/intent). research_runs/steps/sources have
-- no dashboard surface yet; their operator-read lands with the slice that surfaces them (no policy for
-- a surface that does not exist). No BEGIN/COMMIT — internal/pgmigrate applies each file atomically.
--
-- NOTE (migration ledger): 0017 was pencilled for roadmap news/market in the R&I docs; this needed
-- Slice 26 refinement takes the next sequential number, shifting roadmap news→0018, CRM→0019. The
-- ledger stays strictly sequential with no duplicates.

CREATE POLICY research_dossiers_operator_read ON research_dossiers
    FOR SELECT USING (app_current_role() = 'operator');

CREATE POLICY intent_scores_operator_read ON intent_scores
    FOR SELECT USING (app_current_role() = 'operator');

-- Release-blocker (run as a NON-superuser against a real Postgres; superusers bypass RLS):
--   -- tenant_user of B still fail-closed to own tenant:
--   SET app.current_tenant='B'; SET app.current_role='tenant_user';
--   SELECT count(*) FROM research_dossiers WHERE tenant_id='A';   -- MUST be 0
--   -- operator reads across tenants:
--   SET app.current_tenant='platform'; SET app.current_role='operator';
--   SELECT count(*) FROM research_dossiers;                        -- MUST see A's + B's rows
