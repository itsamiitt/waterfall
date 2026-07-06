//go:build integration

// P7 overview integration proofs (doc 12 §P7) over migrations 0001–0010 under FORCE RLS as a
// NON-superuser role:
//
//   - TestOverviewSnapshotThenDelta (acceptance #3): GET /v1/admin/overview snapshot +
//     subsequent overview.tiles.tick SSE events converge to the same values the per-tile
//     endpoints report (all three serve the leader aggregator's persisted 2s tick).
//   - TestOverviewAggregatorFailover (acceptance #5): two aggregator instances; killing the
//     leader lets the other acquire the advisory lock within one tick interval; SSE clients on
//     the surviving instance stay connected across the handover.
//
// Invoke via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set.
package overview_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/overview"
	"github.com/enrichment/waterfall/internal/dash/realtime"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

const appRole = "dash_ov"

func dsnCfg(t *testing.T) pg.Config {
	t.Helper()
	d := os.Getenv("WATERFALL_PG_DSN")
	if d == "" {
		t.Skip("set WATERFALL_PG_DSN to run the overview integration tests")
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
	mustExec(t, admin, `insert into tenants (id, name, kind, status) values ('tenant-a','tenant-a','customer','active')`)
}

func appStore(t *testing.T, cfg pg.Config) (*db.Store, func()) {
	t.Helper()
	c := cfg
	c.User = appRole
	pool := pg.NewPool(c, 8)
	return db.New(pool), pool.Close
}

type opAuthenticator struct{}

func (opAuthenticator) Authenticate(*http.Request) (tenant.Principal, error) {
	return tenant.Principal{TenantID: "platform", UserID: "op-1", Scopes: []string{"role:operator"}}, nil
}

// seedTelemetry loads deterministic tile inputs: 2 providers, 2 keys, today's/yesterday's
// rollups, a queue sample, workers, and a cost rollup.
func seedTelemetry(t *testing.T, admin *pg.Conn) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	mustExec(t, admin, `insert into providers (id, display_name, op_state, credits_remaining) values
		('hunter', 'Hunter', 'enabled', 1000), ('prospeo', 'Prospeo', 'disabled', 500)`)
	mustExec(t, admin, `insert into secret_envelopes (id, kind, master_key_id, dek_wrapped, nonce, ciphertext)
		values ('00000000-0000-4000-8000-0000000000aa', 'provider_key', 'mk1', '\x00', '\x00', '\x00')`)
	mustExec(t, admin, `insert into provider_keys (id, provider_id, secret_envelope_id, status) values
		('00000000-0000-4000-8000-0000000000ab', 'hunter', '00000000-0000-4000-8000-0000000000aa', 'active'),
		('00000000-0000-4000-8000-0000000000ac', 'hunter', '00000000-0000-4000-8000-0000000000aa', 'auth_failed')`)
	mustExec(t, admin, `insert into provider_stats_1d (provider_id, bucket_start, req, ok, credits_spent) values
		('hunter', $1, 900, 855, 990), ('hunter', $2, 800, 700, 880)`, today, today.AddDate(0, 0, -1))
	mustExec(t, admin, `insert into provider_stats_1m (provider_id, bucket_start, req, ok) values
		('hunter', date_trunc('minute', now()), 120, 114)`)
	mustExec(t, admin, `insert into workers (id, status, desired_state) values
		('w1', 'running', 'running'), ('w2', 'lost', 'running')`)
	mustExec(t, admin, `insert into queue_stats_1m (queue, bucket_start, depth, running, scheduled, delayed, retry, failed, dead, enq, deq, oldest_age_s)
		values ('enrich-default', date_trunc('minute', now()), 50, 10, 0, 0, 5, 3, 7, 60, 55, 341)`)
	mustExec(t, admin, `insert into cost_rollup_1d (tenant_id, provider_id, workflow_key, country, day, credits, calls, successful_results)
		values ('tenant-a', 'hunter', 'wf1', 'us', $1, 990, 900, 855)`, today.Format("2006-01-02"))
}

func TestOverviewSnapshotThenDelta(t *testing.T) {
	admin, err := pg.Connect(dsnCfg(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	seedTelemetry(t, admin)

	store, closeStore := appStore(t, dsnCfg(t))
	defer closeStore()
	selfmon := realtime.NewSelfMon(store)
	agg := overview.NewAggregator(store, selfmon, overview.Config{TickInterval: 100 * time.Millisecond}, nil, nil)
	agg.Start(context.Background())
	defer agg.Stop()
	hub := realtime.NewHub(realtime.HubConfig{}, nil)
	poller := realtime.NewPoller(store, hub, realtime.PollerConfig{Interval: 50 * time.Millisecond}, nil)
	poller.Start(context.Background())
	defer poller.Stop()

	mux := http.NewServeMux()
	overview.Routes(mux, overview.Deps{Aggregator: agg, Store: store, Auth: opAuthenticator{}})
	realtime.Routes(mux, realtime.Deps{Hub: hub, Auth: opAuthenticator{}, Config: realtime.StreamConfig{HeartbeatInterval: time.Hour}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Wait for the leader's first persisted tick to serve real values.
	var snap struct {
		GeneratedAt string                     `json:"generated_at"`
		Tiles       map[string]json.RawMessage `json:"tiles"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(srv.URL + "/v1/admin/overview")
		if err != nil {
			t.Fatalf("GET /overview: %v", err)
		}
		body := json.NewDecoder(resp.Body)
		if err := body.Decode(&snap); err != nil {
			t.Fatalf("decode: %v", err)
		}
		resp.Body.Close()
		if agg.Leader() && snap.GeneratedAt != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("aggregator never produced a snapshot")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(snap.Tiles) != 19 {
		t.Fatalf("snapshot has %d tiles, want 19", len(snap.Tiles))
	}

	// Tile values reflect the seeded sources.
	var reqToday struct {
		Value    int64    `json:"value"`
		DeltaPct *float64 `json:"delta_pct"`
	}
	if err := json.Unmarshal(snap.Tiles["requests_today"], &reqToday); err != nil {
		t.Fatal(err)
	}
	if reqToday.Value != 900 || reqToday.DeltaPct == nil || *reqToday.DeltaPct != 12.5 {
		t.Fatalf("requests_today = %+v, want 900 / +12.5%%", reqToday)
	}
	var keys struct{ Total, Active, Failed int64 }
	if err := json.Unmarshal(snap.Tiles["keys_summary"], &keys); err != nil {
		t.Fatal(err)
	}
	if keys.Total != 2 || keys.Active != 1 || keys.Failed != 1 {
		t.Fatalf("keys_summary = %+v", keys)
	}
	var qh struct {
		Queue  string `json:"queue"`
		ValueS int64  `json:"value_s"`
	}
	if err := json.Unmarshal(snap.Tiles["queue_health"], &qh); err != nil {
		t.Fatal(err)
	}
	if qh.Queue != "enrich-default" || qh.ValueS != 341 {
		t.Fatalf("queue_health = %+v", qh)
	}

	// Per-tile endpoint returns the same data + the doc 09 drill pointer.
	resp, err := http.Get(srv.URL + "/v1/admin/overview/tiles/queue_health")
	if err != nil {
		t.Fatal(err)
	}
	var tile struct {
		Tile  string          `json:"tile"`
		Data  json.RawMessage `json:"data"`
		Drill *overview.Drill `json:"drill"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tile); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var qh2 struct {
		Queue  string `json:"queue"`
		ValueS int64  `json:"value_s"`
	}
	if err := json.Unmarshal(tile.Data, &qh2); err != nil {
		t.Fatal(err)
	}
	if qh2 != qh {
		t.Fatalf("tile endpoint %+v != snapshot %+v (must converge)", qh2, qh)
	}
	if tile.Drill == nil || tile.Drill.Endpoint != "GET /v1/admin/queues/{name}/stats" {
		t.Fatalf("drill = %+v", tile.Drill)
	}

	// Unknown tile -> 404 not_found.
	resp, err = http.Get(srv.URL + "/v1/admin/overview/tiles/nope")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown tile status = %d, want 404", resp.StatusCode)
	}

	// Snapshot-then-delta: the next overview.tiles.tick converges to the same tile values.
	ctx, cancelStream := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/admin/streams?topics=overview", nil)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer sresp.Body.Close()
	sc := bufio.NewScanner(sresp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var event, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data = line[len("data: "):]
		case line == "" && event == "overview.tiles.tick":
			var env struct {
				V       int `json:"v"`
				Payload struct {
					GeneratedAt string                     `json:"generated_at"`
					Tiles       map[string]json.RawMessage `json:"tiles"`
				} `json:"payload"`
			}
			if err := json.Unmarshal([]byte(data), &env); err != nil {
				t.Fatalf("tick envelope: %v", err)
			}
			if env.V != 1 || len(env.Payload.Tiles) != 19 {
				t.Fatalf("tick envelope v=%d tiles=%d", env.V, len(env.Payload.Tiles))
			}
			var tickQH struct {
				Queue  string `json:"queue"`
				ValueS int64  `json:"value_s"`
			}
			if err := json.Unmarshal(env.Payload.Tiles["queue_health"], &tickQH); err != nil {
				t.Fatal(err)
			}
			if tickQH != qh {
				t.Fatalf("tick %+v != snapshot %+v (must converge)", tickQH, qh)
			}
			var tickReq struct {
				Value int64 `json:"value"`
			}
			if err := json.Unmarshal(env.Payload.Tiles["requests_today"], &tickReq); err != nil {
				t.Fatal(err)
			}
			if tickReq.Value != reqToday.Value {
				t.Fatalf("tick requests_today = %d, want %d", tickReq.Value, reqToday.Value)
			}
			return // converged
		case line == "":
			event, data = "", ""
		}
	}
	t.Fatal("no overview.tiles.tick observed")
}

func TestOverviewAggregatorFailover(t *testing.T) {
	admin, err := pg.Connect(dsnCfg(t))
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)
	seedTelemetry(t, admin)

	// Two independent stores = two "instances" (separate pools, separate advisory-lock conns).
	storeA, closeA := appStore(t, dsnCfg(t))
	defer closeA()
	storeB, closeB := appStore(t, dsnCfg(t))
	defer closeB()

	const tick = 150 * time.Millisecond
	aggA := overview.NewAggregator(storeA, realtime.NewSelfMon(storeA), overview.Config{TickInterval: tick}, nil, nil)
	aggA.Start(context.Background())
	waitFor(t, 5*time.Second, aggA.Leader, "A never became leader")

	aggB := overview.NewAggregator(storeB, realtime.NewSelfMon(storeB), overview.Config{TickInterval: tick}, nil, nil)
	aggB.Start(context.Background())
	defer aggB.Stop()
	time.Sleep(3 * tick)
	if aggB.Leader() {
		t.Fatal("B grabbed leadership while A held the lock")
	}

	// Survivor instance B serves SSE; a client is connected across the failover.
	hubB := realtime.NewHub(realtime.HubConfig{}, nil)
	pollerB := realtime.NewPoller(storeB, hubB, realtime.PollerConfig{Interval: 50 * time.Millisecond}, nil)
	pollerB.Start(context.Background())
	defer pollerB.Stop()
	mux := http.NewServeMux()
	realtime.Routes(mux, realtime.Deps{Hub: hubB, Auth: opAuthenticator{}, Config: realtime.StreamConfig{HeartbeatInterval: time.Hour}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancelStream := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStream()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/admin/streams?topics=overview", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()

	ticks := make(chan string, 64) // generated_at of each observed tick
	streamClosed := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var event, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				event = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				data = line[len("data: "):]
			case line == "":
				if event == "overview.tiles.tick" {
					var env struct {
						Payload struct {
							GeneratedAt string `json:"generated_at"`
						} `json:"payload"`
					}
					_ = json.Unmarshal([]byte(data), &env)
					ticks <- env.Payload.GeneratedAt
				}
				event, data = "", ""
			}
		}
		streamClosed <- sc.Err()
	}()

	// A tick flows while A leads (B's poller fans out A's persisted snapshot).
	select {
	case <-ticks:
	case err := <-streamClosed:
		t.Fatalf("stream closed before first tick: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no tick while A led")
	}

	// KILL the leader: Stop releases the advisory lock (a crash releases it via conn close).
	aggA.Stop()
	start := time.Now()
	waitFor(t, 5*time.Second, aggB.Leader, "B never acquired leadership after A died")
	takeover := time.Since(start)
	if takeover > 2*tick+500*time.Millisecond {
		t.Errorf("takeover took %v, want within ~one tick interval (%v)", takeover, tick)
	}

	// The surviving instance's subscriber is STILL connected and receives post-failover ticks.
	drain(ticks)
	select {
	case <-ticks:
		// tick produced by B after takeover reached the still-open stream
	case err := <-streamClosed:
		t.Fatalf("SSE client on survivor was disconnected by failover: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no tick after failover")
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(msg)
}

func drain(ch chan string) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
