//go:build integration

package pg_test

import (
	"os"
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
)

// TestConn_SCRAM authenticates against a password (SCRAM-SHA-256) role. Requires a cluster
// where the role uses scram auth, e.g.
//
//	WATERFALL_PG_SCRAM_DSN="host=127.0.0.1 port=55432 user=scram_user password=... dbname=postgres"
func TestConn_SCRAM(t *testing.T) {
	d := os.Getenv("WATERFALL_PG_SCRAM_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_SCRAM_DSN to run the SCRAM auth test")
	}
	c, err := pg.Connect(pg.ParseDSN(d))
	if err != nil {
		t.Fatalf("SCRAM connect failed: %v", err)
	}
	defer c.Close()
	res, err := c.Query("select current_user")
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] == nil {
		t.Fatalf("query after SCRAM auth failed: %v", err)
	}
}

// TestConn_TLS connects over TLS and confirms (via pg_stat_ssl) the backend sees an encrypted
// connection. Requires a cluster with ssl=on and a DSN carrying sslmode=require, e.g.
//
//	WATERFALL_PG_TLS_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres sslmode=require"
func TestConn_TLS(t *testing.T) {
	d := os.Getenv("WATERFALL_PG_TLS_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_TLS_DSN to run the TLS test")
	}
	cfg := pg.ParseDSN(d)
	if cfg.TLS == nil {
		t.Fatalf("sslmode in DSN should have set cfg.TLS")
	}
	c, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("TLS connect failed: %v", err)
	}
	defer c.Close()
	res, err := c.Query("select ssl from pg_stat_ssl where pid = pg_backend_pid()")
	if err != nil {
		t.Fatalf("pg_stat_ssl query: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] == nil || *res.Rows[0][0] != "t" {
		t.Fatalf("connection is not TLS-encrypted per pg_stat_ssl: %+v", res.Rows)
	}
}
