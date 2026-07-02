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
		"drop function if exists app_current_tenant() cascade",
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
		"drop function if exists app_current_tenant() cascade",
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
