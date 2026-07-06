//go:build integration

package pgmigrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgmigrate"
)

// TestApply_NoTransactionEscapeHatch exercises OI-DB-1 end-to-end against an ephemeral Postgres:
//
//  1. a `-- pgmigrate: no-transaction` migration containing CREATE INDEX CONCURRENTLY (which
//     ERRORS inside a transaction block) applies successfully and is recorded;
//  2. the SAME CIC without the directive — i.e. under the atomic wrap — FAILS, proving the escape
//     hatch is load-bearing, not a no-op; and
//  3. a directive-less migration whose second statement fails rolls back ATOMICALLY (its first
//     statement leaves nothing behind and no schema_migrations row is written).
//
// The test runs in a dedicated schema on its own connection so it never collides with the shared
// public-schema state the sibling pgmigrate tests drop and rebuild.
func TestApply_NoTransactionEscapeHatch(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the no-transaction migration test")
	}
	c, err := pg.Connect(pg.ParseDSN(dsn))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	const schema = "pgmigrate_notxn_test"
	if err := c.Exec("drop schema if exists " + schema + " cascade"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := c.Exec("create schema " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Exec("set search_path to public")
		_ = c.Exec("drop schema if exists " + schema + " cascade")
	})
	if err := c.Exec("set search_path to " + schema); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	writeMig := func(dir, name, sql string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	indexExists := func(name string) bool {
		t.Helper()
		res, err := c.Query("select 1 from pg_indexes where schemaname = '" + schema + "' and indexname = '" + name + "'")
		if err != nil {
			t.Fatalf("query pg_indexes: %v", err)
		}
		return len(res.Rows) > 0
	}
	tableExists := func(name string) bool {
		t.Helper()
		res, err := c.Query("select 1 from pg_tables where schemaname = '" + schema + "' and tablename = '" + name + "'")
		if err != nil {
			t.Fatalf("query pg_tables: %v", err)
		}
		return len(res.Rows) > 0
	}
	recorded := func(version string) bool {
		t.Helper()
		res, err := c.Query("select 1 from schema_migrations where version = '" + version + "'")
		if err != nil {
			t.Fatalf("query schema_migrations: %v", err)
		}
		return len(res.Rows) > 0
	}

	// (1) no-transaction CIC applies.
	{
		dir := t.TempDir()
		writeMig(dir, "0001_base.sql", "create table widgets (id int primary key, name text);\n")
		writeMig(dir, "0002_cic.sql",
			"-- pgmigrate: no-transaction\n"+
				"create index concurrently widgets_name_idx on widgets (name);\n")
		applied, err := pgmigrate.Apply(c, dir)
		if err != nil {
			t.Fatalf("apply no-transaction CIC migration: %v", err)
		}
		if len(applied) != 2 || applied[1] != "0002_cic.sql" {
			t.Fatalf("expected [0001_base.sql 0002_cic.sql] applied, got %v", applied)
		}
		if !indexExists("widgets_name_idx") {
			t.Fatal("CREATE INDEX CONCURRENTLY did not create widgets_name_idx")
		}
		if !recorded("0002_cic.sql") {
			t.Fatal("no-transaction migration not recorded in schema_migrations")
		}
	}

	// (2) control: the SAME CIC under the atomic wrap (no directive) must FAIL.
	{
		dir := t.TempDir()
		writeMig(dir, "0003_cic_atomic.sql",
			"create index concurrently widgets_name_idx2 on widgets (name);\n")
		if _, err := pgmigrate.Apply(c, dir); err == nil {
			t.Fatal("CREATE INDEX CONCURRENTLY inside the atomic wrap unexpectedly succeeded")
		}
		if indexExists("widgets_name_idx2") {
			t.Fatal("atomic CIC left an index behind despite failing")
		}
		if recorded("0003_cic_atomic.sql") {
			t.Fatal("failed atomic migration was recorded in schema_migrations")
		}
	}

	// (3) a directive-less migration whose 2nd statement fails rolls back atomically.
	{
		dir := t.TempDir()
		writeMig(dir, "0004_boom.sql",
			"create table rollme (id int);\n"+
				"insert into rollme values (1);\n"+
				"select does_not_exist from rollme;\n")
		applied, err := pgmigrate.Apply(c, dir)
		if err == nil {
			t.Fatal("failing atomic migration unexpectedly succeeded")
		}
		if len(applied) != 0 {
			t.Fatalf("failed migration should report nothing applied, got %v", applied)
		}
		if tableExists("rollme") {
			t.Fatal("atomic rollback failed: rollme table persisted after a failing migration")
		}
		if recorded("0004_boom.sql") {
			t.Fatal("failed atomic migration was recorded in schema_migrations")
		}
	}
}
