// Command dashboardd is the Waterfall admin dashboard server (Phase P0 skeleton). It wires the
// dual-GUC db.Store, the envelope-encryption secrets backend, the identity/session services, and
// the per-Tenant audit hash chain into the internal/dash/httpx admin surface under /v1/admin,
// plus /healthz /readyz /metrics.
//
// It connects as a NON-superuser, non-BYPASSRLS role so tenant isolation (G1) is actually
// enforced at runtime; a startup self-check refuses to run otherwise. With POSTGRES_ADMIN_DSN set
// it bootstraps migrations + roles first so a fresh cluster comes up ready.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/dash/alerts"
	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/cost"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/health"
	"github.com/enrichment/waterfall/internal/dash/httpx"
	"github.com/enrichment/waterfall/internal/dash/keys"
	"github.com/enrichment/waterfall/internal/dash/overview"
	"github.com/enrichment/waterfall/internal/dash/providers"
	"github.com/enrichment/waterfall/internal/dash/queues"
	"github.com/enrichment/waterfall/internal/dash/realtime"
	"github.com/enrichment/waterfall/internal/dash/rotation"
	"github.com/enrichment/waterfall/internal/dash/routing"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/dash/telemetry"
	"github.com/enrichment/waterfall/internal/dash/workers"
	"github.com/enrichment/waterfall/internal/dash/workflows"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgmigrate"
	"github.com/enrichment/waterfall/internal/pgoutbox"
	"github.com/enrichment/waterfall/internal/tenant"
)

// defaultQueueName is the ONE logical queue name the single pgoutbox outbox maps to (OI-QW-8):
// the queue_stats fold and the queue list attribute the aggregate state vector to this
// queue_defs row; multi-queue topology arrives with the target engines (doc 06 §1.3).
const defaultQueueName = "enrich-default"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration; refusing to start", "err", err.Error())
		os.Exit(1)
	}

	// Master keyring + fingerprint pepper for envelope encryption (doc 05 §7).
	keyring, err := secrets.NewKeyring(cfg.masterKey)
	if err != nil {
		logger.Error("master key", "err", err)
		os.Exit(1)
	}

	// Optional admin bootstrap: migrations + least-privileged roles (ops step; convenient for demos).
	if cfg.adminDSN != "" {
		if err := bootstrap(logger, cfg.adminDSN); err != nil {
			logger.Error("postgres bootstrap failed", "err", err)
			os.Exit(1)
		}
	}

	appCfg := pg.ParseDSN(cfg.dsn)
	if err := startupSelfCheck(appCfg); err != nil {
		logger.Error("postgres startup self-check failed", "err", err)
		os.Exit(1)
	}

	pool := pg.NewPool(appCfg, 16)
	defer pool.Close()
	store := db.New(pool)

	backend := secrets.NewPGBackend(store, keyring, cfg.pepper)
	users := security.NewUsers(store, backend, cfg.issuer)
	sessions := security.NewSessions(store)
	ipallow := security.NewIPAllow(store)
	access := security.NewAccessLog(store, 4096)
	access.Start(500 * time.Millisecond)
	defer access.Stop()
	auditLog := audit.New(store)

	var verifier *auth.Verifier
	if cfg.jwtSecret != "" {
		verifier = auth.NewVerifier(auth.WithIssuer(cfg.jwtIssuer), auth.WithAudience(cfg.jwtAudience))
		verifier.AddHMACKey(cfg.jwtKid, []byte(cfg.jwtSecret))
		logger.Info("JWT machine auth enabled", "issuer", cfg.jwtIssuer, "kid", cfg.jwtKid)
	}
	authr := httpx.NewSessionOrJWT(sessions, verifier)

	reg := metrics.New()
	srv := httpx.NewServer(httpx.Deps{
		Store:          store,
		Auth:           authr,
		Users:          users,
		Sessions:       sessions,
		IPAllow:        ipallow,
		Access:         access,
		Secrets:        backend,
		Audit:          auditLog,
		Metrics:        reg,
		TrustedProxies: cfg.trustedProxies,
		Ready:          readyCheck(pool, keyring),
		Issuer:         cfg.issuer,
		Logger:         logger,
	})

	// Session reaper (doc 05 §4.1): deletes rows 24h past expiry/revocation. Expiry is enforced at
	// authentication time regardless, so a lagging reaper never extends a session.
	reaperStop := make(chan struct{})
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-reaperStop:
				return
			case <-t.C:
				rctx := context.Background()
				if err := sessions.DeleteExpired(rctx); err != nil {
					logger.Warn("session reaper", "err", err)
				}
				// TOTP single-use markers (OI-SEC-8): forensic slack only, correctness needs ~90s.
				if err := users.DeleteUsedStepsBefore(rctx, time.Now().Add(-10*time.Minute)); err != nil {
					logger.Warn("mfa used-step reaper", "err", err)
				}
				// Durable admin idempotency ledger (OI-API-8): 24h retention (doc 04 §1.3).
				if err := srv.ReapIdempotency(rctx, time.Now().Add(-24*time.Hour)); err != nil {
					logger.Warn("idempotency reaper", "err", err)
				}
			}
		}
	}()
	defer close(reaperStop)

	// P1 feature routes (providers, keys/pools). Each package owns its RBAC + idempotency + audit
	// and reads the Principal from ctx via httpx.CtxAuthenticator; the shared FeatureChain supplies
	// the single authentication plus IP-allowlist, CSRF, and MFA-gate enforcement so feature routes
	// get the same protections as the P0 surface without re-authenticating (doc 12 P1).
	// P2 rotation engine: the single LeaseResolver instance is shared across the egress path
	// (providers.Deps.Resolver — closing OI-KEYS-2: provider/key test/health-check probes now do a
	// live bounded provider.Call with a resolved key) and rotation.Routes (so selection-state
	// reflects the same live PoolState cache). Its background re-band + refresh loops start here.
	// Telemetry usage recorder — created before the rotation engine so its Lease.Done path can feed
	// usage_events (OI-P4-1). The BufferedRecorder never blocks the hot path (bounded channel,
	// drop-with-metric on overflow). rotation.UsageSample maps 1:1 onto telemetry.UsageEvent.
	usageRecorder := telemetry.NewBufferedRecorder(telemetry.NewRecorder(store), telemetry.BufferedConfig{}, reg)
	usageRecorder.Start(context.Background())
	defer usageRecorder.Stop()

	rotEngine := rotation.New(rotation.Config{
		Store:   rotation.NewStore(store),
		Audit:   auditLog,
		Secrets: rotation.NewSecretOpener(backend),
		Bandit:  bandit.New(),
		Now:     time.Now,
		Logger:  logger,
		// OI-P4-1: every completed lease attributes one usage_events row to its key_id. Customer
		// workflow_key/country populate once enrichd routes real traffic through leases; dashboard-
		// initiated leases (health/test/benchmark) attribute to the platform Tenant.
		RecordUsage: func(ev rotation.UsageSample) {
			usageRecorder.Record(context.Background(), telemetry.UsageEvent{
				TenantID: ev.TenantID, ProviderID: ev.ProviderID, KeyID: ev.KeyID,
				WorkflowKey: ev.WorkflowKey, Country: ev.Country,
				OutcomeClass: ev.OutcomeClass, Credits: ev.Credits, LatMs: ev.LatMs,
			})
		},
	})
	rotEngine.Start()
	defer rotEngine.Stop()

	fmux := http.NewServeMux()

	// P3 config versioning (modules 6+7): ONE shared configver lifecycle engine services both the
	// routing_policy and waterfall_workflow kinds via their injected validators. BumpEpoch is wired
	// to rotation.Engine.Invalidate for the pool-affecting sentinel kinds — closing OI-KEYS-4: the
	// P3 config-epoch watcher hook the P2 rotation engine documented. Built here (before the feature
	// routes) so the P4 approvals executors can bind to it.
	provSrc := provLookup{store: providers.NewPGStore(store)}
	budgetSrc := configver.NewBudgetReader(store)
	cfgSvc := configver.New(configver.Config{
		Store: configver.NewPGStore(store),
		Audit: auditLog,
		Validators: map[string]configver.Validator{
			configver.KindRoutingPolicy:     routing.NewValidator(provSrc, budgetSrc, time.Now),
			configver.KindWaterfallWorkflow: workflows.NewValidator(provSrc, budgetSrc, time.Now),
		},
		OnBump: func(tenantID, kind, scopeKey string) {
			// Only the pool-affecting sentinel kinds invalidate in-memory PoolState (doc 07 §10);
			// routing/workflow publishes bump their own epoch (resolver caches land in later phases).
			switch kind {
			case "key_pool", "provider_catalog":
				rotEngine.Invalidate(scopeKey)
			}
		},
		Now: time.Now,
	})

	// P4 telemetry backbone (doc 10 §2): the leader-elected Loops chassis (aggregator fold +
	// partition maintainer + key-budget reconcile). The startup EnsurePartitions is best-effort here
	// (the app role must OWN the 0009 telemetry tables to run DDL); a failure is alertable but must
	// not wedge boot. The usageRecorder feeding this (OI-P4-1) is created above and wired into the
	// rotation engine's Lease.Done path.
	telemLoops := telemetry.NewLoops(store, time.Now, reg, telemetry.LoopConfig{})
	if err := telemLoops.Start(context.Background()); err != nil {
		logger.Warn("telemetry loops startup (partition ensure)", "err", err)
	}
	defer telemLoops.Stop()

	// P4 approvals quorum engine (doc 05 §9): the Gate in front of the most dangerous writes. The
	// orchestrator registers one Executor per gated action_kind (each drives the real service method
	// under the approval request id as its Idempotency-Key), wires the Service as the Gate into the
	// destructive/publish surfaces, and runs the expirer. Services below are built once here so the
	// executors can reach them (they are stateless over the shared Store, so a second instance
	// alongside each Routes() call is fine).
	provSvc := providers.NewService(providers.Deps{
		Store: store, Audit: auditLog, Resolver: rotEngine, Now: time.Now,
	})
	keysSvc := keys.NewService(store, backend, auditLog, logger)
	apprSvc := approvals.NewService(approvals.Config{
		Store:   store,
		Audit:   auditLog,
		Roster:  approvals.NewRoster(store),
		Tenants: tenantSource{store: store},
		Now:     time.Now,
		Logger:  logger,
	})
	apprSvc.RegisterExecutor(approvals.ActionProviderDelete, func(ctx context.Context, payload json.RawMessage) error {
		id, err := pinnedID(payload)
		if err != nil {
			return err
		}
		return provSvc.Delete(ctx, id)
	})
	apprSvc.RegisterExecutor(approvals.ActionProviderArchive, func(ctx context.Context, payload json.RawMessage) error {
		id, err := pinnedID(payload)
		if err != nil {
			return err
		}
		_, err = provSvc.Archive(ctx, id)
		return err
	})
	apprSvc.RegisterExecutor(approvals.ActionKeyBulkDelete, func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		_, _, err := keysSvc.BulkOp(ctx, keys.BulkInput{IDs: p.IDs, Op: "delete"})
		return err
	})
	apprSvc.RegisterExecutor(approvals.ActionRoutingPublish, configExecutor(cfgSvc, configver.KindRoutingPolicy))
	apprSvc.RegisterExecutor(approvals.ActionWorkflowPublish, configExecutor(cfgSvc, configver.KindWaterfallWorkflow))
	apprSvc.Start()
	defer apprSvc.Stop()

	// Feature routes, now with the approvals Gate wired into the destructive/publish surfaces
	// (providers DELETE/archive, keys bulk delete, routing/workflows publish+rollback). A nil Gate
	// would default to NopGate; here every gated surface shares the one Service.
	providers.Routes(fmux, providers.Deps{
		Store: store, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
		Secrets: backend, Resolver: rotEngine, Gate: apprSvc, Now: time.Now, Logger: logger,
	})
	keys.Routes(fmux, keys.Deps{
		Store: store, Secrets: backend, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
		StepUp: &totpStepUp{users: users, now: time.Now}, Gate: apprSvc, Logger: logger,
	})
	// Rotation's key-pools/{id}/selection-state + key-pools/{id}/simulate coexist with keys'
	// key-pools routes on the SAME fmux — net/http 1.22 disambiguates by full pattern.
	rotation.Routes(fmux, rotation.Deps{Engine: rotEngine, Auth: httpx.CtxAuthenticator{}, Logger: logger})
	routing.Routes(fmux, routing.Deps{
		Service: cfgSvc, Providers: provSrc, Auth: httpx.CtxAuthenticator{}, Gate: apprSvc, Logger: logger,
	})
	workflows.Routes(fmux, workflows.Deps{
		Service: cfgSvc, Providers: provSrc, Auth: httpx.CtxAuthenticator{}, Gate: apprSvc, Logger: logger,
	})

	// Approvals + change-history surface (doc 04 §2.12): deciders drive requests to quorum here,
	// gated by this package's authenticate -> RBAC -> idempotency -> step-up chain (X-MFA-Code
	// verified via the same TOTP step-up the keys surface uses).
	approvals.Routes(fmux, approvals.Deps{
		Service: apprSvc, Auth: httpx.CtxAuthenticator{},
		StepUp: &totpStepUp{users: users, now: time.Now}, Logger: logger,
	})

	// P4 Provider Health Center (doc 04 §2.5): the health surface + the scheduled-probe scheduler and
	// the auto re-enable Reactivator. Reactivation delegates to rotation's KM-3 machine through the
	// keyReactivator adapter (health never imports rotation).
	healthDeps := health.Deps{
		Store: store, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
		Resolver: rotEngine, Reactivator: keyReactivator{eng: rotEngine},
		Logger: logger, Now: time.Now,
	}
	health.Routes(fmux, healthDeps)
	healthSched := health.NewScheduler(healthDeps)
	healthSched.Start()
	defer healthSched.Stop()
	if react := health.NewReactivator(healthDeps); react != nil {
		reactCtx, reactCancel := context.WithCancel(context.Background())
		defer reactCancel()
		go react.Start(reactCtx, time.Minute)
	}

	// P5 queues + workers (modules 8+9, doc 04 §2.8/§2.9, doc 06). Order matters: queues FIRST —
	// its Service is the queue_defs.desired_replicas single writer (doc 06 §5, OI-QW-3) that the
	// workers scale endpoints delegate to. The Outbox is a pgoutbox.Store opened over the SAME
	// non-BYPASSRLS app DSN (exactly how the P5 queues integration tests construct it): redrive
	// is a tenant-scoped guarded UPDATE under the caller's Principal, so no relay privilege is
	// needed — or wanted — on this path.
	outbox, err := pgoutbox.Open(appCfg, 4)
	if err != nil {
		logger.Error("open job outbox", "err", err)
		os.Exit(1)
	}
	defer outbox.Close()
	queueDeps := queues.Deps{
		Store: store, Outbox: outbox, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
		Metrics: reg, Logger: logger,
	}
	qsvc := queues.Routes(fmux, queueDeps)
	// GET /bulk-jobs/{id} single owner (OI-KEYS-1b): queues' DURABLE bulk_jobs reader. The keys
	// package's P1 in-process registration was retired with this wiring — net/http 1.22 panics
	// on a duplicate pattern, and the durable row is the one poller for every 202 bulk job.
	queues.BulkJobsRoute(fmux, queueDeps, qsvc)
	workers.Routes(fmux, workers.Deps{
		Store: store, Audit: auditLog, Auth: httpx.CtxAuthenticator{},
		Scaler: qsvc, Now: time.Now, Logger: logger,
	})

	// Worker-lost detector loop (doc 06 §4): server-derived status='lost' after 3 missed 10s
	// heartbeats, 2-pass hysteresis. workers is Class P (platform-only RLS), so the loop runs
	// under the platform system Principal — the same shape the telemetry folds bind.
	detector := workers.NewDetector(workers.DetectorConfig{
		Store: workers.NewStore(store), Logger: logger, Metrics: reg,
	})
	detector.Start(tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "platform", Scopes: []string{"role:operator"},
	}))
	defer detector.Stop()

	// Bulk-jobs janitor (OI-KEYS-1b): one leader (advisory lock) sweeps expired-lease running
	// bulk_jobs to terminal 'failed', releasing the one-in-flight index a dead instance would wedge.
	bulkJanitor := qsvc.NewJanitor(0)
	bulkJanitor.Start(context.Background())
	defer bulkJanitor.Stop()

	// P7 realtime seam (ADR-0019): the per-instance SSE Hub + the SelfMon store — sole writer
	// of the migration-0010 self_monitor row-set. Constructed before the samplers below so the
	// queue sampler can persist its snapshot through it (closing OI-QW-9).
	sseHub := realtime.NewHub(realtime.HubConfig{}, reg)
	selfmon := realtime.NewSelfMon(store)

	// P5 queue_stats sampler (doc 06 §6, OI-QW-7/8/9): a leader-guarded 15s tick drives the
	// aggregator's QueueStatsFold for the single logical pgoutbox queue. Gating on the telemetry
	// Loops leadership keeps ONE sampler across N instances without a second advisory lock, and
	// the fold is idempotent (last-sample-wins REPLACE), so a handover double-sample is harmless.
	// P7 closes OI-QW-9: each sample is also persisted to the self_monitor queue_stats_sample
	// row, which followers serve and every instance's realtime poller coalesces into
	// queue.stats.tick.
	queueFoldStop := make(chan struct{})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-queueFoldStop:
				return
			case <-t.C:
				if !telemLoops.Leader() {
					continue
				}
				if err := telemLoops.Aggregator().QueueStatsFold(context.Background(), defaultQueueName, time.Now()); err != nil {
					logger.Warn("queue stats fold", "err", err)
					continue
				}
				if err := realtime.SnapshotQueueStats(context.Background(), store, selfmon); err != nil {
					logger.Warn("queue stats self_monitor snapshot", "err", err)
				}
			}
		}
	}()
	defer close(queueFoldStop)

	// P6 cost analytics + alerts (modules 10+12; doc 12 §P6). Cost is read-only over the rollups;
	// alerts run two leader-elected loops (evaluator 30s, notifier 5s) with their own advisory
	// locks. Channel configs are sealed envelopes; webhook/SMTP delivery goes through the
	// SSRF-guarded egress path. Doctrine: budgets alert, the G4 Cost Ceiling enforces.
	costSvc := cost.NewService(store, time.Now)
	cost.Routes(fmux, cost.Deps{Service: costSvc, Auth: httpx.CtxAuthenticator{}, Audit: auditLog, Logger: logger})

	alertStore := alerts.NewStore(store, time.Now)
	alertEval := alerts.NewEvaluator(alertStore, auditLog, time.Now, reg, logger)
	alertNotif := alerts.NewNotifier(alertStore, backend, nil /* SSRF-guarded default egress */, time.Now, reg, logger)
	alertSvc := alerts.NewService(alerts.Config{
		Store: alertStore, Secrets: backend, Audit: auditLog,
		Eval: alertEval, Notifier: alertNotif, Now: time.Now,
	})
	alerts.Routes(fmux, alerts.Deps{
		Service: alertSvc, Auth: httpx.CtxAuthenticator{},
		StepUp: &totpStepUp{users: users, now: time.Now}, Logger: logger,
	})
	alertLoops := alerts.NewLoops(store, alertEval, alertNotif, alerts.LoopConfig{})
	alertLoops.Start(context.Background())
	defer alertLoops.Stop()

	// P7 overview + SSE realtime (module 1 + ADR-0019, doc 12 §P7). The overview Aggregator is
	// the leader-elected 2s tile loop persisting to self_monitor('overview_snapshot'); the
	// realtime Poller on THIS instance derives the closed event vocabulary from the DB and fans
	// out through the Hub (DB reads O(instances), never O(clients)); the streams endpoint is a
	// GET and rides the FeatureChain's session auth WITHOUT CSRF (safe-method exemption).
	ovAgg := overview.NewAggregator(store, selfmon, overview.Config{}, reg, logger)
	ovAgg.Start(context.Background())
	defer ovAgg.Stop()
	overview.Routes(fmux, overview.Deps{
		Aggregator: ovAgg, Store: store, Auth: httpx.CtxAuthenticator{},
		Audit: auditLog, Logger: logger,
	})
	sseStreams := realtime.Routes(fmux, realtime.Deps{
		Hub: sseHub, Auth: httpx.CtxAuthenticator{},
		Config: realtime.StreamConfig{
			MaxConns:      cfg.sseMaxConns,
			WriteDeadline: cfg.sseWriteDeadline,
		},
		Logger: logger,
	})
	poller := realtime.NewPoller(store, sseHub, realtime.PollerConfig{}, logger)
	poller.Start(context.Background())
	defer poller.Stop()
	// LISTEN/NOTIFY wake (timeboxed extension, ADR-0019): lower-latency poller wakes via
	// pg_notify('dash_config', ...); best-effort — the poll interval remains the contract.
	stopWaker := realtime.StartNotifyWaker(context.Background(), appCfg, poller, logger)
	defer stopWaker()
	// Self-monitor publisher (closes OI-P6-2): per-instance SSE client counts + the telemetry
	// fold watermark land in self_monitor every 15s, so the P6 system.sse_clients /
	// system.aggregator_lag_s alert metrics evaluate against live rows.
	stopMonitor := realtime.StartMonitor(context.Background(), selfmon, sseStreams,
		realtime.MonitorConfig{Watermark: telemLoops.Aggregator().Watermark}, reg, logger)
	defer stopMonitor()

	featureHandler := srv.FeatureChain(fmux)

	// Compose: feature path subtrees route to featureHandler; everything else (P0 admin routes,
	// /healthz /readyz /metrics) routes to the P0 handler. net/http 1.22 subtree patterns take
	// precedence over "/", so /v1/admin/providers/{id} lands on featureHandler.
	admin := http.NewServeMux()
	admin.Handle("/", srv.Handler())
	for _, p := range []string{"providers", "keys", "key-pools", "key-imports", "bulk-jobs", "rotation",
		"routing", "workflows", "config", "health", "approvals", "change-history",
		"queues", "dead-letters", "jobs", "workers", "cost", "budgets", "alerts",
		"streams", "overview", "search", "meta"} {
		admin.Handle("/v1/admin/"+p, featureHandler)
		admin.Handle("/v1/admin/"+p+"/", featureHandler)
	}

	var handler http.Handler = admin
	// Serve the built SPA if present (P8 adds web/dist); skipped when absent.
	if st, err := os.Stat("web/dist"); err == nil && st.IsDir() {
		mux := http.NewServeMux()
		mux.Handle("/v1/", handler)
		mux.Handle("/healthz", handler)
		mux.Handle("/readyz", handler)
		mux.Handle("/metrics", handler)
		mux.Handle("/", http.FileServer(http.Dir("web/dist")))
		handler = mux
		logger.Info("serving web/dist statically")
	}

	addr := fmt.Sprintf(":%d", cfg.port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		logger.Info("dashboardd listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// totpStepUp verifies a per-request X-MFA-Code against the caller's TOTP seed for step-up-gated
// actions (doc 05 §5.4). The keys package calls VerifyStepUp before its create/import/rotate/bulk
// handlers; a non-nil error becomes 403 mfa_required. The seed is opened from the caller's sealed
// envelope via the secrets backend inside security.Users.TOTPSeed; recovery-code acceptance on the
// step-up path is deferred (OI-SEC-9).
type totpStepUp struct {
	users *security.Users
	now   func() time.Time
}

var errStepUpFailed = errors.New("step-up verification failed")

func (v *totpStepUp) VerifyStepUp(ctx context.Context, code string) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err
	}
	// VerifyAndConsume also records the (user, time_step) single-use marker, so a step-up code
	// cannot be replayed within its window (OI-SEC-8), exactly like the login MFA path.
	ok, err := v.users.VerifyAndConsume(ctx, p.UserID, code, v.now())
	if err != nil {
		return err
	}
	if !ok {
		return errStepUpFailed
	}
	return nil
}

// tenantSource enumerates active tenants for the approvals expirer sweep (RLS requires a bound
// tenant per UPDATE). It reads the operator-readable tenants registry under PlatformTx; the sweep
// covers 'platform' too (operator-level requests like provider_delete live under it).
type tenantSource struct{ store *db.Store }

func (t tenantSource) ActiveTenantIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := t.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, qerr := c.Query("select id from tenants where status = 'active'")
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			if r[0] != nil {
				ids = append(ids, *r[0])
			}
		}
		return nil
	})
	return ids, err
}

// keyReactivator adapts the rotation Engine into health.KeyReactivator: health decides WHICH
// exhausted/rate_limited keys to probe and calls Probe, which drives the KM-3 recovery edge in
// rotation. This one adapter is the only rotation touch-point in the health path.
type keyReactivator struct{ eng *rotation.Engine }

func (a keyReactivator) Probe(ctx context.Context, keyID string) error {
	return a.eng.ReactivateKey(ctx, keyID)
}

// pinnedID reads {"id":"..."} from a pinned approval payload.
func pinnedID(payload json.RawMessage) (string, error) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// configExecutor builds the approvals Executor for a config kind (routing_policy / waterfall_workflow):
// it parses the pinned publish/rollback payload and drives the real configver lifecycle method. The
// approval request id rides ctx (RequestIDFromContext) for the underlying method's idempotency; the
// approvals engine already guarantees exactly-once by running this Executor once under the quorum lock.
func configExecutor(cfgSvc *configver.Service, kind string) approvals.Executor {
	return func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			Op        string  `json:"op"`
			VersionID string  `json:"version_id"`
			ScopeKey  string  `json:"scope_key"`
			ToVersion int     `json:"to_version"`
			Expected  *string `json:"expected"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return err
		}
		if p.Op == "rollback" {
			_, err := cfgSvc.Rollback(ctx, kind, p.ScopeKey, p.ToVersion, p.Expected)
			return err
		}
		_, err := cfgSvc.Publish(ctx, p.VersionID, p.Expected)
		return err
	}
}

// provLookup adapts the providers PGStore into a configver.ProviderSource for the routing/workflow
// validators + dry-run. It reads the FULL provider row (via PlatformTx, so it works regardless of
// the caller's Principal — validators need op_state / compliance / sunset which the tenant catalog
// projection omits) and maps only non-secret catalog metadata + capabilities. A missing provider is
// (false, nil), never an error, so VR-1 (provider_unknown) reports it as a rule finding.
type provLookup struct {
	store *providers.PGStore
}

var _ configver.ProviderSource = provLookup{}

func (p provLookup) Lookup(ctx context.Context, id string) (configver.ProviderInfo, bool, error) {
	pr, err := p.store.GetFull(ctx, id)
	if errors.Is(err, providers.ErrNotFound) {
		return configver.ProviderInfo{}, false, nil
	}
	if err != nil {
		return configver.ProviderInfo{}, false, err
	}
	caps := make([]configver.Capability, 0, len(pr.Capabilities))
	for _, c := range pr.Capabilities {
		caps = append(caps, configver.Capability{
			Field: c.Field, Cost: c.CostCredits, ExpectedConfidence: c.ExpectedConfidence,
		})
	}
	return configver.ProviderInfo{
		ID:           pr.ID,
		Status:       pr.Status,
		OpState:      pr.OpState,
		Compliance:   pr.ComplianceReviewStatus,
		SunsetAt:     pr.SunsetAt,
		Capabilities: caps,
	}, true, nil
}

// readyCheck returns a /readyz probe: PG reachable + master key present (doc 12 P0).
func readyCheck(pool *pg.Pool, kr *secrets.Keyring) func(context.Context) error {
	return func(ctx context.Context) error {
		if id, _ := kr.Active(); id == "" {
			return errors.New("no active master key")
		}
		c, err := pool.Get(ctx)
		if err != nil {
			return err
		}
		defer pool.Put(c, false)
		return c.Exec("select 1")
	}
}

// bootstrap applies migrations and provisions the least-privileged app role over a privileged
// admin connection (ops step; here it makes the binary self-bootstrapping for a demo).
func bootstrap(logger *slog.Logger, adminDSN string) error {
	admin, err := pg.Connect(pg.ParseDSN(adminDSN))
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	defer admin.Close()

	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = "migrations"
	}
	applied, err := pgmigrate.Apply(admin, dir)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.Info("migrations applied", "newly_applied", len(applied), "dir", dir)

	if err := admin.Exec(provisionRolesSQL); err != nil {
		return fmt.Errorf("provision roles: %w", err)
	}
	logger.Info("dash app role provisioned")
	return nil
}

// provisionRolesSQL creates the RLS-enforced app role and grants the dashboard table privileges it
// needs. Idempotent. Requires a superuser admin connection.
const provisionRolesSQL = `
do $$
begin
  if not exists (select 1 from pg_roles where rolname = 'dash_app') then
    create role dash_app login nosuperuser;
  end if;
end
$$;
grant select, insert, update, delete on
  tenants, users, mfa_recovery_codes, sessions, ip_allowlists,
  audit_log, audit_chain_heads, api_access_log, secret_envelopes to dash_app;
grant usage on sequence audit_log_id_seq, api_access_log_id_seq to dash_app;
-- P5 queues + workers (doc 06): worker registry + heartbeats, queue registry/scale intent,
-- durable bulk jobs, and the folded queue/worker stats.
grant select, insert, update, delete on
  workers, queue_defs, bulk_jobs, queue_stats_1m, queue_stats_1h,
  worker_heartbeats, worker_stats_5m to dash_app;
-- job_outbox: the queues read model + the pgoutbox redrive verbs only (select, update under
-- FORCE RLS) — inserts stay with the enrichment API's role (one-owner-per-table, doc 06 §2.4).
grant select, update on job_outbox to dash_app;
-- P1 providers + keys (0005).
grant select, insert, update, delete on
  providers, key_pools, provider_keys, key_pool_members, key_budgets,
  key_import_batches, health_schedules, rotation_triggers to dash_app;
grant select on providers_catalog to dash_app;
-- P3 config versioning (0006, incl. budgets per D-2).
grant select, insert, update, delete on
  config_versions, config_active, config_epochs, workflow_index, budgets to dash_app;
-- P4 alerts/approvals (0007) + telemetry (0009).
grant select, insert, update, delete on
  alert_channels, alert_rules, alert_events, alert_notifications,
  approval_policies, approval_requests, approval_decisions to dash_app;
grant select, insert, update, delete on
  usage_events, provider_stats_1m, provider_stats_1h, provider_stats_1d,
  key_usage_1m, key_usage_1h, key_usage_1d, tenant_usage_1h, tenant_usage_1d,
  cost_rollup_1d, provider_health_checks, provider_health_1d to dash_app;
-- 0011 hardening closeout (TOTP replay guard + durable admin idempotency).
grant select, insert, update, delete on mfa_used_steps, dash_admin_idempotency to dash_app;
-- P7 self_monitor snapshot row-set (0010): loop heartbeats, fold watermarks, SSE client
-- counts, overview/queue snapshots — written through internal/dash/realtime.SelfMon only.
grant select, insert, update, delete on self_monitor to dash_app;
`

// startupSelfCheck refuses to run as a role that bypasses RLS (which would silently defeat G1) and
// verifies the dashboard schema is present.
func startupSelfCheck(appCfg pg.Config) error {
	c, err := pg.Connect(appCfg)
	if err != nil {
		return fmt.Errorf("connect as app role %q: %w", appCfg.User, err)
	}
	defer c.Close()

	super, bypassRLS, err := c.RolePrivileges()
	if err != nil {
		return fmt.Errorf("check role privileges: %w", err)
	}
	if super || bypassRLS {
		return fmt.Errorf("app connects as role %q which bypasses row-level security "+
			"(superuser=%v, bypassrls=%v); tenant isolation (G1) would NOT be enforced", appCfg.User, super, bypassRLS)
	}
	var missing []string
	for _, tbl := range []string{"users", "sessions", "audit_log", "secret_envelopes"} {
		if err := c.Exec("select 1 from " + tbl + " where false"); err != nil {
			missing = append(missing, tbl)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tables missing (run migrations): %v", missing)
	}
	return nil
}

// --- configuration ---

type config struct {
	port             int
	dsn              string
	adminDSN         string
	masterKey        string
	pepper           []byte
	issuer           string
	jwtSecret        string
	jwtIssuer        string
	jwtAudience      string
	jwtKid           string
	trustedProxies   []*net.IPNet
	sseMaxConns      int           // SSE_MAX_CONNS (doc 04 §3.5; default 500)
	sseWriteDeadline time.Duration // SSE_WRITE_DEADLINE (default 10s)
}

// loadConfig reads and validates configuration from the environment, reporting every problem at
// once. It is pure (getenv in, config+error out) for testability.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{port: 8090, issuer: "Waterfall", jwtKid: "default"}
	var errs []error

	if v := getenv("PORT"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("PORT %q is not a valid port", v))
		} else {
			c.port = n
		}
	}
	c.dsn = getenv("POSTGRES_DSN")
	if c.dsn == "" {
		errs = append(errs, errors.New("POSTGRES_DSN is required"))
	}
	c.adminDSN = getenv("POSTGRES_ADMIN_DSN")

	c.masterKey = getenv("DASH_MASTER_KEY")
	if c.masterKey == "" {
		errs = append(errs, errors.New("DASH_MASTER_KEY is required"))
	}
	if p := getenv("DASH_FINGERPRINT_PEPPER"); p != "" {
		c.pepper = []byte(p)
	} else {
		c.pepper = []byte("waterfall-dash-default-pepper") // dev default; set DASH_FINGERPRINT_PEPPER in prod
	}
	if v := getenv("DASH_ISSUER"); v != "" {
		c.issuer = v
	}

	c.jwtSecret = getenv("JWT_HS256_SECRET")
	if c.jwtSecret != "" {
		if len(c.jwtSecret) < 16 {
			errs = append(errs, errors.New("JWT_HS256_SECRET must be at least 16 bytes"))
		}
		c.jwtIssuer = getenv("JWT_ISSUER")
		c.jwtAudience = getenv("JWT_AUDIENCE")
		if k := getenv("JWT_KID"); k != "" {
			c.jwtKid = k
		}
	}

	// SSE deployment knobs (doc 04 §3.5, doc 11 §2): connection cap + per-write deadline.
	if v := getenv("SSE_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("SSE_MAX_CONNS %q is not a positive integer", v))
		} else {
			c.sseMaxConns = n
		}
	}
	if v := getenv("SSE_WRITE_DEADLINE"); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("SSE_WRITE_DEADLINE %q is not a positive duration", v))
		} else {
			c.sseWriteDeadline = d
		}
	}

	for _, cidr := range strings.Split(getenv("DASH_TRUSTED_PROXIES"), ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			errs = append(errs, fmt.Errorf("DASH_TRUSTED_PROXIES entry %q: %w", cidr, err))
			continue
		}
		c.trustedProxies = append(c.trustedProxies, n)
	}

	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, nil
}
