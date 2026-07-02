//go:build integration

package pg_test

import (
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
)

// dsn returns the test DSN or skips. Set e.g.
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres"
func dsn(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run Postgres integration tests")
	}
	return pg.ParseDSN(d)
}

func TestConn_SimpleAndExtended(t *testing.T) {
	c, err := pg.Connect(dsn(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// simple query
	res, err := c.Query("select 1 as one, 'hi' as greet")
	if err != nil {
		t.Fatalf("simple query: %v", err)
	}
	if len(res.Rows) != 1 || res.Cols[0] != "one" || *res.Rows[0][0] != "1" || *res.Rows[0][1] != "hi" {
		t.Fatalf("unexpected simple result: %+v", res)
	}

	// DDL + parameterized insert/select round-trip
	if err := c.Exec("create temp table t (id int, name text, note text)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.ExecParams("insert into t (id, name, note) values ($1,$2,$3)", 42, "alice", nil); err != nil {
		t.Fatalf("insert params: %v", err)
	}
	got, err := c.QueryParams("select id, name, note from t where id = $1", 42)
	if err != nil {
		t.Fatalf("select params: %v", err)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(got.Rows))
	}
	row := got.Rows[0]
	if *row[0] != "42" || *row[1] != "alice" || row[2] != nil {
		t.Fatalf("round-trip mismatch: %v %v null=%v", *row[0], *row[1], row[2] == nil)
	}

	// error surfaces as PGError
	if err := c.Exec("select * from no_such_table"); err == nil {
		t.Fatal("expected error for missing table")
	}
	// connection still usable after an error (Sync recovered)
	if err := c.Exec("select 1"); err != nil {
		t.Fatalf("connection not usable after error: %v", err)
	}
}
