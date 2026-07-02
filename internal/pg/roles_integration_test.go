//go:build integration

// Live test for the startup G1-safety primitive: RolePrivileges must correctly report whether
// the connected role can bypass row-level security (superuser or BYPASSRLS). Set WATERFALL_PG_DSN
// (as a superuser, so the test can create the other roles).
package pg_test

import (
	"testing"

	"github.com/enrichment/waterfall/internal/pg"
)

func TestRolePrivileges(t *testing.T) {
	admin := dsn(t) // superuser
	c, err := pg.Connect(admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer c.Close()

	// The admin connection is a superuser -> bypasses RLS.
	if super, _, err := c.RolePrivileges(); err != nil || !super {
		t.Fatalf("admin should be superuser: super=%v err=%v", super, err)
	}

	// Provision a least-privileged role and a BYPASSRLS role.
	_ = c.Exec("drop role if exists rp_app")
	_ = c.Exec("drop role if exists rp_bypass")
	if err := c.Exec("create role rp_app login nosuperuser"); err != nil {
		t.Fatalf("create rp_app: %v", err)
	}
	if err := c.Exec("create role rp_bypass login nosuperuser bypassrls"); err != nil {
		t.Fatalf("create rp_bypass: %v", err)
	}
	defer func() { _ = c.Exec("drop role if exists rp_app"); _ = c.Exec("drop role if exists rp_bypass") }()

	// The least-privileged role does NOT bypass RLS — the state the app requires.
	appCfg := admin
	appCfg.User = "rp_app"
	ac, err := pg.Connect(appCfg)
	if err != nil {
		t.Fatalf("connect rp_app: %v", err)
	}
	defer ac.Close()
	if super, bypass, err := ac.RolePrivileges(); err != nil || super || bypass {
		t.Fatalf("rp_app must not bypass RLS: super=%v bypass=%v err=%v", super, bypass, err)
	}

	// The BYPASSRLS role is detected — this is what the startup self-check rejects.
	bypassCfg := admin
	bypassCfg.User = "rp_bypass"
	bc, err := pg.Connect(bypassCfg)
	if err != nil {
		t.Fatalf("connect rp_bypass: %v", err)
	}
	defer bc.Close()
	if super, bypass, err := bc.RolePrivileges(); err != nil || super || !bypass {
		t.Fatalf("rp_bypass must report bypassrls: super=%v bypass=%v err=%v", super, bypass, err)
	}
}
