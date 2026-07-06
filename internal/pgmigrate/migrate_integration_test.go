//go:build integration

package pgmigrate_test

import (
	"os"
	"strconv"
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgmigrate"
)

func TestApply_OrderedAndIdempotent(t *testing.T) {
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the migration-runner test")
	}
	c, err := pg.Connect(pg.ParseDSN(d))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// Clean slate.
	for _, s := range []string{
		"drop table if exists schema_migrations, job_outbox, field_versions, idempotency_ledger, cost_ledger cascade",
		// Migration 0004 (dashboard identity/RBAC) tables + sequences + role GUC helper, so a
		// forced full re-apply starts from a genuine clean slate.
		"drop table if exists tenants, users, mfa_recovery_codes, sessions, ip_allowlists, audit_log, audit_chain_heads, api_access_log, secret_envelopes cascade",
		// Migration 0005 (dashboard providers/keys) tables + the catalog view, so a forced
		// clean-slate re-apply does not hit "relation already exists".
		"drop view if exists providers_catalog cascade",
		"drop table if exists providers, key_pools, provider_keys, key_pool_members, key_budgets, key_import_batches, health_schedules, rotation_triggers cascade",
		// Migration 0006 (config versioning) tables + budgets (moved here per Deviation D-2).
		"drop table if exists config_versions, config_active, config_epochs, workflow_index, budgets cascade",
		// Migrations 0007 (alerts/approvals), 0008 (workers/queues), 0009 (telemetry/rollups);
		// partitioned parents drop their partitions via cascade.
		"drop table if exists alert_channels, alert_rules, alert_events, alert_notifications, approval_policies, approval_requests, approval_decisions cascade",
		"drop table if exists workers, queue_defs, bulk_jobs cascade",
		// Migration 0010 (self_monitor snapshot row-set).
		"drop table if exists self_monitor cascade",
		"drop table if exists usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d, key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d, cost_rollup_1d, queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m, provider_health_checks, provider_health_1d cascade",
		"drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade",
		"drop function if exists app_current_tenant() cascade",
		"drop function if exists app_current_role() cascade",
	} {
		_ = c.Exec(s)
	}

	applied, err := pgmigrate.Apply(c, "../../migrations")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Applied in filename order; assert the known prefix (later migrations may be appended).
	want := []string{"0001_init.sql", "0002_job_outbox.sql", "0003_outbox_dlq.sql"}
	if len(applied) < len(want) {
		t.Fatalf("expected at least %d migrations, got %v", len(want), applied)
	}
	for i, w := range want {
		if applied[i] != w {
			t.Fatalf("migration %d out of order: got %q want %q (%v)", i, applied[i], w, applied)
		}
	}

	// The migrations actually created their tables + the DLQ column.
	if err := c.Exec("select 1 from field_versions where false"); err != nil {
		t.Fatalf("field_versions missing after migrate: %v", err)
	}
	if err := c.Exec("select attempts, dead from job_outbox where false"); err != nil {
		t.Fatalf("job_outbox DLQ columns missing after migrate: %v", err)
	}

	// Re-running is a no-op.
	again, err := pgmigrate.Apply(c, "../../migrations")
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-apply should be a no-op, got %v", again)
	}

	res, err := c.Query("select count(*) from schema_migrations")
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] == nil || *res.Rows[0][0] != strconv.Itoa(len(applied)) {
		t.Fatalf("schema_migrations should record %d versions, got %+v (err=%v)", len(applied), res.Rows, err)
	}

	// After applying everything, nothing is pending.
	if pend, err := pgmigrate.Pending(c, "../../migrations"); err != nil || len(pend) != 0 {
		t.Fatalf("Pending should be empty after a full apply, got %v (err=%v)", pend, err)
	}
}

// TestPending_ReportsUnapplied checks the startup self-check primitive: on a virgin database
// every migration is pending; after applying, none is.
func TestPending_ReportsUnapplied(t *testing.T) {
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the migration-runner test")
	}
	c, err := pg.Connect(pg.ParseDSN(d))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()
	for _, s := range []string{
		"drop table if exists schema_migrations, job_outbox, field_versions, idempotency_ledger, cost_ledger cascade",
		// Migration 0004 (dashboard identity/RBAC) tables + sequences + role GUC helper, so a
		// forced full re-apply starts from a genuine clean slate.
		"drop table if exists tenants, users, mfa_recovery_codes, sessions, ip_allowlists, audit_log, audit_chain_heads, api_access_log, secret_envelopes cascade",
		// Migration 0005 (dashboard providers/keys) tables + the catalog view, so a forced
		// clean-slate re-apply does not hit "relation already exists".
		"drop view if exists providers_catalog cascade",
		"drop table if exists providers, key_pools, provider_keys, key_pool_members, key_budgets, key_import_batches, health_schedules, rotation_triggers cascade",
		// Migration 0006 (config versioning) tables + budgets (moved here per Deviation D-2).
		"drop table if exists config_versions, config_active, config_epochs, workflow_index, budgets cascade",
		// Migrations 0007 (alerts/approvals), 0008 (workers/queues), 0009 (telemetry/rollups);
		// partitioned parents drop their partitions via cascade.
		"drop table if exists alert_channels, alert_rules, alert_events, alert_notifications, approval_policies, approval_requests, approval_decisions cascade",
		"drop table if exists workers, queue_defs, bulk_jobs cascade",
		// Migration 0010 (self_monitor snapshot row-set).
		"drop table if exists self_monitor cascade",
		"drop table if exists usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d, key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d, cost_rollup_1d, queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m, provider_health_checks, provider_health_1d cascade",
		"drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade",
		"drop function if exists app_current_tenant() cascade",
		"drop function if exists app_current_role() cascade",
	} {
		_ = c.Exec(s)
	}

	// Virgin DB (no schema_migrations table) -> everything pending, in order.
	pend, err := pgmigrate.Pending(c, "../../migrations")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pend) < 3 || pend[0] != "0001_init.sql" {
		t.Fatalf("virgin DB should report all migrations pending, got %v", pend)
	}

	if _, err := pgmigrate.Apply(c, "../../migrations"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if pend, err := pgmigrate.Pending(c, "../../migrations"); err != nil || len(pend) != 0 {
		t.Fatalf("no migration should be pending after apply, got %v (err=%v)", pend, err)
	}
}
