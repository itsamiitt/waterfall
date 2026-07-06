//go:build integration

// P7 realtime integration proofs (doc 12 §P7):
//
//   - TestSSESoakLite (acceptance #1, soak-lite): 200 concurrent SSE clients against an
//     httptest server for a SHORT bounded window; p99 tick-to-receipt <= 2s; ZERO dropped
//     `*.changed` events (every client sees every changed event, in order). The full 10-minute
//     soak is the P12 harness (doc 13 §6; doc 12 OI row).
//   - TestSelfMonitorRLS: self_monitor (migration 0010) is Class P — a tenant-bound
//     transaction reads zero rows and cannot write (G1 release-blocker shape).
//   - TestPollerDerivesEvents: the read-poller derives the closed event vocabulary from real
//     DB mutations (providers/keys watermark, self_monitor snapshots, bulk_jobs progress).
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set (TestSSESoakLite needs no DB).
package realtime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/realtime"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_rt"

func dsnCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the realtime integration tests")
	}
	return pg.ParseDSN(d)
}

func mustExec(t *testing.T, c *pg.Conn, sql string, args ...any) {
	t.Helper()
	if err := c.ExecParams(sql, args...); err != nil {
		t.Fatalf("exec %.70q: %v", sql, err)
	}
}

func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	for _, s := range []string{
		"drop owned by " + appRole + " cascade",
		"drop role if exists " + appRole,
		"drop table if exists self_monitor cascade",
		"drop table if exists job_outbox, workers, queue_defs, bulk_jobs cascade",
		"drop table if exists queue_stats_1m, queue_stats_1h, worker_heartbeats, worker_stats_5m cascade",
		"drop table if exists usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d cascade",
		"drop table if exists key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d cascade",
		"drop table if exists cost_rollup_1d, provider_health_checks, provider_health_1d cascade",
		"drop table if exists alert_channels, alert_rules, alert_events, alert_notifications cascade",
		"drop table if exists approval_policies, approval_requests, approval_decisions cascade",
		"drop table if exists config_versions, config_active, config_epochs, workflow_index, budgets cascade",
		"drop view if exists providers_catalog cascade",
		"drop table if exists providers, key_pools, provider_keys, key_pool_members, key_budgets, key_import_batches, health_schedules, rotation_triggers cascade",
		"drop table if exists field_versions, idempotency_ledger, cost_ledger cascade",
		"drop table if exists tenants, users, mfa_recovery_codes, sessions, ip_allowlists cascade",
		"drop table if exists audit_log, audit_chain_heads, api_access_log, secret_envelopes cascade",
		"drop sequence if exists audit_log_id_seq, api_access_log_id_seq cascade",
		"drop function if exists app_current_role() cascade",
		"drop function if exists app_current_tenant() cascade",
	} {
		_ = admin.Exec(s)
	}
	for _, f := range []string{
		"../../../migrations/0001_init.sql",
		"../../../migrations/0002_job_outbox.sql",
		"../../../migrations/0003_outbox_dlq.sql",
		"../../../migrations/0004_dash_identity_rbac.sql",
		"../../../migrations/0005_dash_providers_keys.sql",
		"../../../migrations/0006_dash_config_versions.sql",
		"../../../migrations/0007_dash_alerts_approvals.sql",
		"../../../migrations/0008_dash_workers_queues.sql",
		"../../../migrations/0009_dash_telemetry.sql",
		"../../../migrations/0010_dash_self_monitor.sql",
	} {
		ddl, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := admin.Exec(string(ddl)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	mustExec(t, admin, "create role "+appRole+" login nosuperuser")
	mustExec(t, admin, "grant select, insert, update, delete on all tables in schema public to "+appRole)
	mustExec(t, admin, "grant usage, select on all sequences in schema public to "+appRole)
	for _, tid := range []string{"tenant-a", "tenant-b"} {
		mustExec(t, admin, `insert into tenants (id, name, kind, status) values ($1,$1,'customer','active')`, tid)
	}
}

func appStore(t *testing.T, cfg pg.Config) (*db.Store, func()) {
	t.Helper()
	c := cfg
	c.User = appRole
	pool := pg.NewPool(c, 8)
	return db.New(pool), pool.Close
}

func opAuth() opAuthenticator { return opAuthenticator{} }

type opAuthenticator struct{}

func (opAuthenticator) Authenticate(*http.Request) (tenant.Principal, error) {
	return tenant.Principal{TenantID: "platform", UserID: "op-1", Scopes: []string{"role:operator"}}, nil
}

// --- acceptance #1 (soak-lite) ---

// soakClient records what one SSE client received.
type soakClient struct {
	tickLatencies []time.Duration
	changedIDs    []int
	disconnected  bool
	err           error
}

func TestSSESoakLite(t *testing.T) {
	const (
		clients      = 200
		window       = 20 * time.Second
		tickEvery    = 250 * time.Millisecond
		changedEvery = 500 * time.Millisecond
	)
	hub := realtime.NewHub(realtime.HubConfig{}, nil)
	mux := http.NewServeMux()
	realtime.Routes(mux, realtime.Deps{Hub: hub, Auth: opAuth(), Config: realtime.StreamConfig{MaxConns: clients + 10}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	results := make([]soakClient, clients)
	var wg sync.WaitGroup
	deadline := time.Now().Add(window)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/admin/streams?topics=overview,key", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results[idx].err = err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				results[idx].err = fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			sc := bufio.NewScanner(resp.Body)
			var event, data string
			for sc.Scan() {
				line := sc.Text()
				switch {
				case strings.HasPrefix(line, "event: "):
					event = line[len("event: "):]
				case strings.HasPrefix(line, "data: "):
					data = line[len("data: "):]
				case line == "" && event != "":
					var env struct {
						Payload map[string]any `json:"payload"`
					}
					_ = json.Unmarshal([]byte(data), &env)
					switch event {
					case "overview.tiles.tick":
						if ns, ok := env.Payload["published_unix_ns"].(float64); ok {
							results[idx].tickLatencies = append(results[idx].tickLatencies,
								time.Duration(time.Now().UnixNano()-int64(ns)))
						}
					case "key.status.changed":
						if n, ok := env.Payload["n"].(float64); ok {
							results[idx].changedIDs = append(results[idx].changedIDs, int(n))
						}
					}
					event, data = "", ""
				}
			}
			// scanner ends when the window deadline cancels the request (expected) or the
			// server force-disconnected the subscriber (a dropped-event violation below).
			if time.Now().Before(deadline.Add(-time.Second)) {
				results[idx].disconnected = true
			}
		}(i)
	}

	// Barrier: publish only after every client is subscribed, so "zero dropped changed
	// events" is assertable as "every client saw the full sequence".
	connectDeadline := time.Now().Add(15 * time.Second)
	for hub.Clients() < clients && time.Now().Before(connectDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.Clients() < clients {
		t.Fatalf("only %d/%d clients connected", hub.Clients(), clients)
	}

	pubCtx, pubCancel := context.WithDeadline(context.Background(), deadline.Add(-2*time.Second))
	defer pubCancel()
	var changedTotal int
	var pubWG sync.WaitGroup
	pubWG.Add(1)
	go func() { // publisher: coalescible ticks + never-droppable changed events
		defer pubWG.Done()
		tick := time.NewTicker(tickEvery)
		changed := time.NewTicker(changedEvery)
		defer tick.Stop()
		defer changed.Stop()
		seq := 0
		for {
			select {
			case <-pubCtx.Done():
				changedTotal = seq
				return
			case <-tick.C:
				hub.Publish(realtime.Event{
					Name:    "overview.tiles.tick",
					Payload: map[string]any{"published_unix_ns": time.Now().UnixNano()},
				})
			case <-changed.C:
				hub.Publish(realtime.Event{
					Name:    "key.status.changed",
					Payload: map[string]any{"n": seq},
				})
				seq++
			}
		}
	}()

	wg.Wait()
	pubCancel()
	pubWG.Wait()

	// Assertions: all clients connected, none force-disconnected, zero dropped changed events
	// (each client saw a gapless prefix-to-suffix run of the changed sequence), p99 <= 2s.
	var lats []time.Duration
	minChanged := changedTotal
	for i := range results {
		r := &results[i]
		if r.err != nil {
			t.Fatalf("client %d failed: %v", i, r.err)
		}
		if r.disconnected {
			t.Fatalf("client %d was disconnected mid-soak (dropped-event path)", i)
		}
		if len(r.tickLatencies) == 0 {
			t.Fatalf("client %d received no ticks", i)
		}
		for j := 1; j < len(r.changedIDs); j++ {
			if r.changedIDs[j] != r.changedIDs[j-1]+1 {
				t.Fatalf("client %d changed-event gap: %d -> %d (changed events must never drop)",
					i, r.changedIDs[j-1], r.changedIDs[j])
			}
		}
		if len(r.changedIDs) < minChanged {
			minChanged = len(r.changedIDs)
		}
		lats = append(lats, r.tickLatencies...)
	}
	if changedTotal < 10 {
		t.Fatalf("publisher emitted only %d changed events; window too short", changedTotal)
	}
	// Every client subscribed from the start; allow the last event to be in flight at cutoff.
	if minChanged < changedTotal-2 {
		t.Fatalf("a client saw only %d of %d changed events", minChanged, changedTotal)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p99 := lats[len(lats)*99/100]
	t.Logf("soak-lite: clients=%d window=%s ticks=%d p50=%s p99=%s changed=%d",
		clients, window, len(lats), lats[len(lats)/2], p99, changedTotal)
	if p99 > 2*time.Second {
		t.Fatalf("p99 tick-to-receipt = %s, want <= 2s", p99)
	}
}

// --- self_monitor RLS (G1) ---

func TestSelfMonitorRLS(t *testing.T) {
	admin, err := pg.Connect(dsnCfg(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	store, closeStore := appStore(t, dsnCfg(t))
	defer closeStore()

	m := realtime.NewSelfMon(store)
	ctx := context.Background()
	if err := m.UpsertSSEClients(ctx, "inst-1", 7); err != nil {
		t.Fatalf("platform upsert: %v", err)
	}
	if err := m.UpsertWatermark(ctx, "usage", time.Now().Add(-3*time.Second)); err != nil {
		t.Fatalf("platform watermark: %v", err)
	}
	if _, err := m.UpsertSnapshot(ctx, "overview_snapshot", "overview", []byte(`{"tiles":{}}`)); err != nil {
		t.Fatalf("platform snapshot: %v", err)
	}

	// Platform reads see the rows (and the P6 system.* metric queries work — OI-P6-2 closed).
	err = store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query("select coalesce(sum(sse_clients),0) from self_monitor")
		if qerr != nil {
			return qerr
		}
		if got := *res.Rows[0][0]; got != "7" {
			t.Errorf("system.sse_clients sum = %s, want 7", got)
		}
		res, qerr = c.Query("select coalesce(extract(epoch from (now() - min(watermark_ts))),0) from self_monitor")
		if qerr != nil {
			return qerr
		}
		if res.Rows[0][0] == nil || *res.Rows[0][0] == "0" {
			t.Error("system.aggregator_lag_s read no watermark")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("platform read: %v", err)
	}

	// Tenant-bound transactions: ZERO rows visible, writes refused (platform-only, FORCE RLS).
	tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "tenant-a", UserID: "u", Scopes: []string{"role:tenant_admin"}})
	err = store.Tx(tctx, func(c *pg.Conn) error {
		res, qerr := c.Query("select count(*) from self_monitor")
		if qerr != nil {
			return qerr
		}
		if got := *res.Rows[0][0]; got != "0" {
			t.Errorf("tenant sees %s self_monitor rows, want 0", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tenant read: %v", err)
	}
	werr := store.Tx(tctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into self_monitor (key, component) values ('evil', 'x')`)
	})
	if werr == nil {
		t.Fatal("tenant write to self_monitor succeeded; want RLS refusal")
	}
}

// --- poller derivation ---

func TestPollerDerivesEvents(t *testing.T) {
	admin, err := pg.Connect(dsnCfg(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	store, closeStore := appStore(t, dsnCfg(t))
	defer closeStore()

	hub := realtime.NewHub(realtime.HubConfig{}, nil)
	poller := realtime.NewPoller(store, hub, realtime.PollerConfig{Interval: 50 * time.Millisecond}, nil)
	poller.Start(context.Background())
	defer poller.Stop()
	time.Sleep(300 * time.Millisecond) // let the seed pass complete

	ch, cancel := hub.Subscribe([]string{"overview", "provider", "key", "import"})
	defer cancel()

	// provider mutation -> provider.health.changed
	mustExec(t, admin, `insert into providers (id, display_name, op_state) values ('hunter', 'Hunter', 'enabled')`)
	// key mutation -> key.status.changed (envelope row satisfies the FK)
	mustExec(t, admin, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext)
		values ('00000000-0000-4000-8000-0000000000aa', 'provider_key', 'mk1', '\x00', '\x00', '\x00')`)
	mustExec(t, admin, `insert into provider_keys (id, provider_id, secret_envelope_id, status)
		values ('00000000-0000-4000-8000-0000000000ab', 'hunter', '00000000-0000-4000-8000-0000000000aa', 'active')`)
	// overview snapshot -> overview.tiles.tick
	m := realtime.NewSelfMon(store)
	if _, err := m.UpsertSnapshot(context.Background(), "overview_snapshot", "overview",
		[]byte(`{"generated_at":"2026-07-05T00:00:00Z","tiles":{}}`)); err != nil {
		t.Fatalf("snapshot upsert: %v", err)
	}
	// platform bulk job -> import.batch.progress
	mustExec(t, admin, `insert into bulk_jobs (id, tenant_id, kind, scope_fingerprint, status, total, succeeded, failed)
		values ('00000000-0000-4000-8000-0000000000ac', 'platform', 'import', 'fp1', 'running', 100, 5, 0)`)

	want := map[string]bool{
		"provider.health.changed": false,
		"key.status.changed":      false,
		"overview.tiles.tick":     false,
		"import.batch.progress":   false,
	}
	deadline := time.After(5 * time.Second)
	for remaining := len(want); remaining > 0; {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatal("subscription closed")
			}
			if seen, tracked := want[e.Name]; tracked && !seen {
				want[e.Name] = true
				remaining--
				switch e.Name {
				case "provider.health.changed":
					if e.Scope["provider_id"] != "hunter" {
						t.Errorf("provider scope = %v", e.Scope)
					}
				case "key.status.changed":
					if e.Scope["provider_id"] != "hunter" || e.Scope["key_id"] == "" {
						t.Errorf("key scope = %v", e.Scope)
					}
				}
			}
		case <-deadline:
			t.Fatalf("missing events: %+v", want)
		}
	}

	// Progress counter change re-emits import.batch.progress.
	mustExec(t, admin, `update bulk_jobs set succeeded = 50 where kind = 'import'`)
	deadline = time.After(5 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Name == "import.batch.progress" {
				var pl struct {
					Succeeded int64 `json:"succeeded"`
				}
				b, _ := json.Marshal(e.Payload)
				_ = json.Unmarshal(b, &pl)
				if pl.Succeeded == 50 {
					return
				}
			}
		case <-deadline:
			t.Fatal("progress delta not observed")
		}
	}
}
