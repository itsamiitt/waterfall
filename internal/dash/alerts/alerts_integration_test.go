//go:build integration

// Live-Postgres proof for the P6 alerting engine (module 12, doc 12 §P6) over migrations 0004 +
// 0007 (alert tables) + 0009 (cost_rollup_1d), under FORCE ROW LEVEL SECURITY as a NON-superuser
// role (superusers bypass RLS, proving nothing):
//
//   - TestAlertsFireDedupeResolve (P6 gate #1 end-to-end + gate #4 dedupe): a sustained breach fires
//     ONE episode + ONE outbox notification; a second evaluator cycle in the same episode adds no
//     row (episode edge-trigger); the partial unique index rejects a duplicate pending enqueue of
//     the same occasion; the notifier delivers it (status=sent); after cooldown a renotify enqueues;
//     recovery resolves after 3 clean cycles and enqueues a resolved notification.
//   - TestAlertsTestSendSSRFBlocked (P6 gate #4 SSRF half): a channel whose webhook target is an
//     RFC1918 address is refused by the SSRF-guarded egress client through the REAL test-send path.
//   - TestAlertsRLSIsolation (P6 gate #5): alert_rules and alert_events are Tenant-isolated.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package alerts_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/alerts"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const alertRole = "dash_alerts"

var alertTables = []string{"alert_notifications", "alert_events", "alert_rules", "alert_channels"}
var alertRollups = []string{
	"cost_rollup_1d", "tenant_usage_1h", "tenant_usage_1d", "key_usage_1m", "key_usage_1h",
	"key_usage_1d", "usage_events", "provider_stats_1m", "provider_stats_1h", "provider_stats_1d",
	"queue_stats_1m", "queue_stats_1h", "worker_heartbeats", "worker_stats_5m",
	"provider_health_checks", "provider_health_1d",
}
var alertIDTables = []string{
	"tenants", "users", "mfa_recovery_codes", "sessions", "ip_allowlists",
	"audit_log", "audit_chain_heads", "api_access_log", "secret_envelopes",
}

func adminCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the alerts integration test")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func tryExec(c *pg.Conn, sql string) { _ = c.Exec(sql) }

func applyMigration(t *testing.T, admin *pg.Conn, path string) {
	t.Helper()
	ddl, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

func setupAlertSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	tryExec(admin, "drop owned by "+alertRole+" cascade")
	tryExec(admin, "drop role if exists "+alertRole)
	tryExec(admin, "drop table if exists key_budgets cascade")
	tryExec(admin, "drop table if exists "+strings.Join(alertTables, ", ")+" cascade")
	tryExec(admin, "drop table if exists approval_decisions, approval_requests, approval_policies cascade")
	tryExec(admin, "drop table if exists "+strings.Join(alertRollups, ", ")+" cascade")
	tryExec(admin, "drop table if exists "+strings.Join(alertIDTables, ", ")+" cascade")
	tryExec(admin, "drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade")
	tryExec(admin, "drop function if exists app_current_role() cascade")
	tryExec(admin, "drop function if exists app_current_tenant() cascade")

	mustExec(t, admin, `create or replace function app_current_tenant() returns text
		language sql stable as $$ select current_setting('app.current_tenant', true) $$`)
	applyMigration(t, admin, "../../../migrations/0004_dash_identity_rbac.sql")
	applyMigration(t, admin, "../../../migrations/0007_dash_alerts_approvals.sql")
	applyMigration(t, admin, "../../../migrations/0009_dash_telemetry.sql")

	mustExec(t, admin, "create role "+alertRole+" login nosuperuser")
	grant := append(append(append([]string{}, alertTables...), alertRollups...), alertIDTables...)
	mustExec(t, admin, "grant select, insert, update, delete on "+strings.Join(grant, ", ")+" to "+alertRole)
	mustExec(t, admin, "grant usage on sequence audit_log_id_seq, api_access_log_id_seq to "+alertRole)

	for _, tid := range []string{"acme", "globex"} {
		mustExec(t, admin, `insert into tenants (id, name, kind, status) values ($1,$1,'customer','active')`, tid)
	}
}

func appStore(t *testing.T, cfg pg.Config) *db.Store {
	appCfg := cfg
	appCfg.User = alertRole
	pool := pg.NewPool(appCfg, 12)
	t.Cleanup(pool.Close)
	return db.New(pool)
}

func newBackend(t *testing.T, store *db.Store) secrets.Backend {
	t.Helper()
	mk := make([]byte, 32)
	_, _ = rand.Read(mk)
	kr, err := secrets.NewKeyring(base64.StdEncoding.EncodeToString(mk))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return secrets.NewPGBackend(store, kr, []byte("test-pepper-alerts"))
}

// testUserIDs maps each Tenant to a valid v4 uuid (alert_rules.created_by / alert_events.ack_by are
// uuid columns; in production UserID is a users.id uuid).
var testUserIDs = map[string]string{
	"acme":   "11111111-1111-4111-8111-111111111111",
	"globex": "22222222-2222-4222-8222-222222222222",
}

func ctxFor(tid, role string) context.Context {
	uid := testUserIDs[tid]
	if uid == "" {
		uid = "33333333-3333-4333-8333-333333333333"
	}
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: tid, UserID: uid, Scopes: []string{"role:" + role},
	})
}

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func countAdmin(t *testing.T, admin *pg.Conn, sql string) int {
	t.Helper()
	res, err := admin.Query(sql)
	if err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return 0
	}
	n, _ := strconv.Atoi(*res.Rows[0][0])
	return n
}

// seedDay writes a cost_rollup_1d row for acme on the given UTC day.
func seedDay(t *testing.T, admin *pg.Conn, day time.Time, credits int64) {
	t.Helper()
	mustExec(t, admin, `insert into cost_rollup_1d
		(tenant_id, provider_id, workflow_key, country, day, credits, calls, successful_results)
		values ('acme','hunter','email','us',$1,$2,10,9)
		on conflict (tenant_id, provider_id, workflow_key, country, day)
		do update set credits = excluded.credits`,
		day.Format("2006-01-02"), credits)
}

// TestAlertsFireDedupeResolve is P6 gate #1 (end-to-end) + gate #4 (dedupe).
func TestAlertsFireDedupeResolve(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupAlertSchema(t, admin)

	store := appStore(t, cfg)
	backend := newBackend(t, store)
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)

	// A local sink for delivery, reached through a permissive factory (the guarded client blocks
	// loopback by design; the SSRF-block path is proven in TestAlertsTestSendSSRFBlocked).
	var delivered int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&delivered, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	as := alerts.NewStore(store, clock)
	eval := alerts.NewEvaluator(as, nil, clock, nil, nil)
	notifier := alerts.NewNotifier(as, backend, func(string) alerts.HTTPDoer { return srv.Client() }, clock, nil, nil)
	svc := alerts.NewService(alerts.Config{Store: as, Secrets: backend, Eval: eval, Notifier: notifier, Now: clock})

	actx := ctxFor("acme", "tenant_admin")
	ch, err := svc.CreateChannel(actx, "webhook", "sink", alerts.ChannelConfig{URL: srv.URL})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	rule, err := svc.CreateRule(actx, alerts.Rule{
		Name: "acme daily spend", Metric: "cost.daily_credits", Op: "gt", Threshold: 1000,
		WindowS: 0, CooldownS: 120, Severity: "warning", Channels: []string{ch.ID}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// Breach: 5000 credits today >> threshold 1000.
	seedDay(t, admin, now, 5000)

	// Cycle 1: fire -> 1 firing episode + 1 pending notification.
	if err := eval.EvaluateOnce(actx); err != nil {
		t.Fatalf("evaluate cycle1: %v", err)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_events where state='firing'"); got != 1 {
		t.Fatalf("firing episodes after cycle1 = %d, want 1", got)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_notifications"); got != 1 {
		t.Fatalf("notifications after cycle1 = %d, want 1", got)
	}

	// Cycle 2: still breaching, cooldown not elapsed -> NO new episode, NO new notification (dedupe).
	if err := eval.EvaluateOnce(actx); err != nil {
		t.Fatalf("evaluate cycle2: %v", err)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_events where state='firing'"); got != 1 {
		t.Fatalf("firing episodes after cycle2 = %d, want 1 (single-firing invariant)", got)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_notifications"); got != 1 {
		t.Fatalf("notifications after cycle2 = %d, want 1 (edge-triggered dedupe)", got)
	}

	// Directly exercise the partial unique index: a duplicate pending enqueue of the same
	// (event, channel, occasion) dedupe_key is a no-op.
	evID := countAdmin(t, admin, "select max(id) from alert_events")
	dk := "dup-dedupe-key-xyz"
	insertDup := func() {
		if err := store.Tx(actx, func(c *pg.Conn) error {
			return c.ExecParams(
				`insert into alert_notifications (tenant_id, event_id, channel_id, dedupe_key, status, next_retry_at)
				 values (app_current_tenant(), $1, $2, $3, 'pending', now())
				 on conflict (dedupe_key) where status='pending' do nothing`,
				evID, ch.ID, dk)
		}); err != nil {
			t.Fatalf("duplicate probe insert: %v", err)
		}
	}
	insertDup()
	insertDup()
	if got := countAdmin(t, admin, "select count(*) from alert_notifications where dedupe_key='"+dk+"'"); got != 1 {
		t.Fatalf("duplicate pending enqueue produced %d rows, want 1 (partial unique index)", got)
	}
	// Clean up the synthetic duplicate probe so later counts are about real occasions.
	mustExec(t, admin, "delete from alert_notifications where dedupe_key=$1", dk)

	// Notifier delivers the pending fired notification.
	if err := notifier.DeliverOnce(actx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if atomic.LoadInt32(&delivered) != 1 {
		t.Fatalf("sink received %d deliveries, want 1", delivered)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_notifications where status='sent'"); got != 1 {
		t.Fatalf("sent notifications = %d, want 1", got)
	}

	// Advance past cooldown and re-evaluate: renotify enqueues exactly one more notification.
	now = now.Add(200 * time.Second)
	clock2 := fixedClock(now)
	eval2 := alerts.NewEvaluator(as, nil, clock2, nil, nil)
	// Rebuild clean cache is fresh; open episode carries notified_at from the DB.
	if err := eval2.EvaluateOnce(actx); err != nil {
		t.Fatalf("evaluate renotify: %v", err)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_notifications"); got != 2 {
		t.Fatalf("notifications after renotify = %d, want 2", got)
	}

	// Recover: no breach; 3 clean cycles resolve the episode and enqueue a resolved notification.
	seedDay(t, admin, now, 0)
	for i := 0; i < 3; i++ {
		if err := eval2.EvaluateOnce(actx); err != nil {
			t.Fatalf("evaluate clean %d: %v", i, err)
		}
	}
	if got := countAdmin(t, admin, "select count(*) from alert_events where state='firing'"); got != 0 {
		t.Fatalf("firing episodes after resolve = %d, want 0", got)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_events where state='resolved'"); got != 1 {
		t.Fatalf("resolved episodes = %d, want 1", got)
	}
	if got := countAdmin(t, admin, "select count(*) from alert_notifications"); got != 3 {
		t.Fatalf("notifications after resolve = %d, want 3 (fired + renotify + resolved)", got)
	}
	_ = rule
}

// TestAlertsTestSendSSRFBlocked is P6 gate #4 (SSRF half) through the real test-send path.
func TestAlertsTestSendSSRFBlocked(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupAlertSchema(t, admin)

	store := appStore(t, cfg)
	backend := newBackend(t, store)
	clock := fixedClock(time.Now())
	as := alerts.NewStore(store, clock)
	// Default egress factory => the real SSRF-guarded provider egress client.
	notifier := alerts.NewNotifier(as, backend, nil, clock, nil, nil)
	svc := alerts.NewService(alerts.Config{Store: as, Secrets: backend, Notifier: notifier, Now: clock})

	actx := ctxFor("acme", "tenant_admin")
	ch, err := svc.CreateChannel(actx, "webhook", "internal", alerts.ChannelConfig{URL: "https://10.0.0.1/hook"})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	_, err = svc.TestChannel(actx, ch.ID)
	if !errors.Is(err, alerts.ErrEgressBlocked) {
		t.Fatalf("test-send to RFC1918 target: err=%v, want ErrEgressBlocked", err)
	}
}

// TestAlertsRLSIsolation is P6 gate #5 (alerts half).
func TestAlertsRLSIsolation(t *testing.T) {
	cfg := adminCfg(t)
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupAlertSchema(t, admin)

	store := appStore(t, cfg)
	backend := newBackend(t, store)
	clock := fixedClock(time.Now())
	as := alerts.NewStore(store, clock)
	svc := alerts.NewService(alerts.Config{Store: as, Secrets: backend, Now: clock})

	// acme creates a rule; globex must not see it.
	if _, err := svc.CreateRule(ctxFor("acme", "tenant_admin"), alerts.Rule{
		Name: "acme rule", Metric: "cost.daily_credits", Op: "gt", Threshold: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("acme create rule: %v", err)
	}
	acmeRules, err := svc.ListRules(ctxFor("acme", "tenant_admin"), "", "", nil)
	if err != nil || len(acmeRules) != 1 {
		t.Fatalf("acme rules = %d (err %v), want 1", len(acmeRules), err)
	}
	globexRules, err := svc.ListRules(ctxFor("globex", "tenant_admin"), "", "", nil)
	if err != nil {
		t.Fatalf("globex list: %v", err)
	}
	if len(globexRules) != 0 {
		t.Fatalf("globex saw acme's rules: %d", len(globexRules))
	}

	// A firing event under acme must be invisible to globex.
	mustExec(t, admin, `insert into alert_events (tenant_id, rule_id, state, dedupe_key)
		values ('acme', $1, 'firing', 'dk')`, acmeRules[0].ID)
	globexEvents, err := svc.ListEvents(ctxFor("globex", "tenant_admin"), "", "", "", nil, nil, 0)
	if err != nil {
		t.Fatalf("globex list events: %v", err)
	}
	if len(globexEvents) != 0 {
		t.Fatalf("globex saw acme's alert_events: %d", len(globexEvents))
	}
}
