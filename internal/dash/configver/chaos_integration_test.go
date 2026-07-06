//go:build integration

// Chaos / fault-injection proofs for the config-versioning publish path and the shared db.Store
// connection pool (doc 13 §7 drills "dashboardd kill during publish" and "PostgreSQL restart";
// closes OI-P12-1 chaos + OI-P12-3 / OI-TS-5):
//
//   - TestPublishCrashFaultPointInvariant: assigns the env-gated, test-only
//     configver.PublishFaultAfterPointer hook to PANIC mid-publish — after config_active has been
//     flipped but before commit. Because the flip + version status + epoch bump + audit all run in
//     ONE transaction, the crash must roll the whole thing back: config_active still points at the
//     prior published version (never a dangling pointer to the non-validated candidate), the
//     candidate stays 'validated', and config_epochs is NOT double-bumped. A clean retry (hook
//     cleared) then publishes successfully — proving forward progress after a crash.
//   - TestPGRestartPoolRecovers: runs a workload through a db.Store pool, then forcibly drops every
//     pooled backend connection (the connection-level effect of a PostgreSQL restart) via an admin
//     pg_terminate_backend sweep, and asserts the hand-rolled internal/pg pool evicts the broken
//     conns and reconnects so subsequent queries succeed.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package configver_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// TestPublishCrashFaultPointInvariant proves publish atomicity under a mid-transaction crash.
func TestPublishCrashFaultPointInvariant(t *testing.T) {
	cfg := cvAdminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupConfigSchema(t, admin)

	// A baseline published version V0 establishes the committed config_active pointer + epoch=1.
	setupStore := appStore(t, cfg)
	svc := newSvc(setupStore)
	acme := ctxFor("acme", "tenant_admin")

	v0, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"v":0}`))
	v0, _ = svc.Validate(acme, v0.ID)
	if _, err := svc.Publish(acme, v0.ID, nil); err != nil {
		t.Fatalf("publish baseline v0: %v", err)
	}
	assertActivePublished(t, setupStore, "acme", "default", v0.ID)
	if e := epoch(t, setupStore, "acme", configver.KindRoutingPolicy); e != 1 {
		t.Fatalf("epoch after baseline = %d, want 1", e)
	}

	// A validated candidate V1 (parented at V0) that we will try to publish — and crash.
	v1, _ := svc.CreateDraft(acme, configver.KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"v":1}`))
	v1, _ = svc.Validate(acme, v1.ID)

	// Publish V1 through a DEDICATED store/pool so the crashed transaction's dirty connection is
	// discarded with that pool (mirrors the process dying) and can never be reused for a read.
	crashPool := pg.NewPool(appCfgFor(cfg), 4)
	crashStore := db.New(crashPool)
	crashSvc := newSvc(crashStore)

	// Fault: panic AFTER the config_active pointer flip, BEFORE commit. Cleanup guarantees the
	// test-only hook is cleared even if an assertion below fails, so it can never leak into the
	// sibling lifecycle tests that share this package's test binary.
	t.Cleanup(func() { configver.PublishFaultAfterPointer = nil })
	fired := false
	configver.PublishFaultAfterPointer = func() {
		fired = true
		panic("simulated dashboardd crash mid-publish")
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected the fault hook to panic mid-publish, but Publish returned normally")
			}
		}()
		_, _ = crashSvc.Publish(acme, v1.ID, strp(v0.ID))
	}()
	configver.PublishFaultAfterPointer = nil // disarm before any further work
	if !fired {
		t.Fatalf("fault hook never fired — the crash was not injected after the pointer flip")
	}
	// Discard the crashed pool: closing it drops the dirty connection, so Postgres rolls back the
	// uncommitted publish transaction and releases the config_active row lock.
	crashPool.Close()

	// INVARIANT 1: config_active still points at the OLD published version V0 (never V1, which was
	// never validated-then-committed through to published) — no dangling pointer.
	assertActivePublished(t, setupStore, "acme", "default", v0.ID)
	// INVARIANT 2: the crashed candidate is still 'validated' — the version status flip rolled back.
	if st := statusOf(t, setupStore, v1.ID); st != configver.StatusValidated {
		t.Fatalf("crashed candidate v1 status = %q, want validated (status flip must have rolled back)", st)
	}
	// INVARIANT 3: config_epochs was NOT bumped by the crashed publish (still 1, not 2).
	if e := epoch(t, setupStore, "acme", configver.KindRoutingPolicy); e != 1 {
		t.Fatalf("epoch after crashed publish = %d, want 1 (epoch must not double-bump on a rolled-back publish)", e)
	}

	// Forward progress: a clean retry (hook disarmed) publishes V1 and advances everything exactly
	// once — the crash left no poison state behind.
	if _, err := crashRetryPublish(t, cfg, acme, v1.ID, v0.ID); err != nil {
		t.Fatalf("clean retry publish of v1: %v", err)
	}
	assertActivePublished(t, setupStore, "acme", "default", v1.ID)
	if st := statusOf(t, setupStore, v0.ID); st != configver.StatusArchived {
		t.Fatalf("v0 status after v1 publish = %q, want archived", st)
	}
	if e := epoch(t, setupStore, "acme", configver.KindRoutingPolicy); e != 2 {
		t.Fatalf("epoch after clean retry = %d, want 2 (exactly one more bump)", e)
	}

	t.Logf("PASS publish-crash: fault fired after pointer flip; rollback held config_active=%s (old), v1=validated, epoch=1; clean retry -> config_active=%s, v0 archived, epoch=2",
		v0.ID, v1.ID)
}

// crashRetryPublish publishes on a fresh store/pool (a clean process after the crash) and returns
// the publish error, proving the retry path is unaffected by the earlier crash.
func crashRetryPublish(t *testing.T, cfg pg.Config, ctx context.Context, versionID, expected string) (configver.Version, error) {
	t.Helper()
	retryStore := appStore(t, cfg)
	return newSvc(retryStore).Publish(ctx, versionID, &expected)
}

// TestPGRestartPoolRecovers proves the internal/pg pool reconnects after every pooled backend is
// dropped (the connection-level effect of a PostgreSQL restart; doc 13 §7 "PostgreSQL restart").
func TestPGRestartPoolRecovers(t *testing.T) {
	cfg := cvAdminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupConfigSchema(t, admin)

	const poolMax = 4
	pool := pg.NewPool(appCfgFor(cfg), poolMax)
	t.Cleanup(pool.Close)
	store := db.New(pool)
	ctx := ctxFor("acme", "tenant_admin")

	// Workload: run several transactions, then force the pool to hold poolMax LIVE idle
	// connections (checking them all out at once, then returning them) so the restart drops every
	// one and recovery must drain multiple broken conns — not just a single reused connection.
	for i := 0; i < poolMax*3; i++ {
		if got := poolCount(t, store, ctx); got < 0 {
			t.Fatalf("pre-restart workload query %d failed", i)
		}
	}
	raw := store.Pool()
	held := make([]*pg.Conn, 0, poolMax)
	for i := 0; i < poolMax; i++ {
		c, err := raw.Get(context.Background())
		if err != nil {
			t.Fatalf("pre-open conn %d: %v", i, err)
		}
		held = append(held, c)
	}
	for _, c := range held {
		raw.Put(c, false) // return live -> pool now caches poolMax reusable connections
	}

	// Simulate the restart: forcibly terminate every dash_cv backend except our admin sweep conn.
	// A real pg_ctl restart drops exactly these connections; the pool must notice and reconnect.
	killed := pgScalar(t, admin, `select count(*) from (
		select pg_terminate_backend(pid) from pg_stat_activity
		 where usename = '`+cvRole+`' and pid <> pg_backend_pid()) s`)
	t.Logf("terminated %s pooled %s backend(s) (simulated PG restart)", killed, cvRole)

	// Recovery: the pool holds now-dead idle conns. Each Get may hand back one dead conn whose
	// BEGIN fails (marked broken, closed, token returned), so up to poolMax transient failures can
	// occur before a fresh dial. Assert the pool self-heals within a bounded number of attempts and
	// then stays healthy — no transparent per-query retry exists, so the first post-restart query
	// may surface one error to the caller (documented in doc 13 §6/§7).
	transientFails := 0
	recovered := false
	for attempt := 0; attempt < poolMax*3+5; attempt++ {
		if err := storeErr(store, ctx); err != nil {
			transientFails++
			continue
		}
		recovered = true
		break
	}
	if !recovered {
		t.Fatalf("pool never recovered after %d attempts (%d transient failures)", poolMax*3+5, transientFails)
	}

	// Steady state: once reconnected, a burst of subsequent queries all succeed.
	for i := 0; i < poolMax*3; i++ {
		if err := storeErr(store, ctx); err != nil {
			t.Fatalf("post-recovery query %d failed (pool did not stay healthy): %v", i, err)
		}
	}

	t.Logf("PASS PG-restart: pool evicted broken conns and reconnected after %d transient failure(s); subsequent queries all succeeded",
		transientFails)
}

// appCfgFor returns cfg re-pointed at the non-superuser app role used by the config suite.
func appCfgFor(cfg pg.Config) pg.Config {
	appCfg := cfg
	appCfg.User = cvRole
	return appCfg
}

// poolCount runs a trivial count through the store and returns the value (or -1 on error).
func poolCount(t *testing.T, store *db.Store, ctx context.Context) int {
	t.Helper()
	n := -1
	err := store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.Query("select count(*) from config_versions")
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			n = 0
			for _, ch := range *res.Rows[0][0] {
				n = n*10 + int(ch-'0')
			}
		}
		return nil
	})
	if err != nil {
		return -1
	}
	return n
}

// storeErr runs a trivial transaction and returns its error (nil on success) without failing the
// test — the caller decides whether an error is a tolerated transient or a hard failure.
func storeErr(store *db.Store, ctx context.Context) error {
	return store.Tx(ctx, func(c *pg.Conn) error {
		_, err := c.Query("select 1")
		return err
	})
}

// pgScalar runs a scalar query on a raw connection and returns the first column of the first row.
func pgScalar(t *testing.T, c *pg.Conn, sql string) string {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return ""
	}
	return *res.Rows[0][0]
}
