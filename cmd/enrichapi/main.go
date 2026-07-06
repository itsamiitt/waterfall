// Command enrichapi runs the HTTP gateway + async worker pool for the Waterfall
// Enrichment Engine. It wires the API (auth→principal, Idempotency-Key, rate limit) to a
// bounded job queue whose workers execute the Router+Engine (G1–G5).
//
// It uses in-memory mock providers so it runs offline; swap in HTTPAdapters for real
// vendors without changing the gateway or queue.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/enrichment/waterfall/internal/api"
	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/config"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/durable"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/heartbeat"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgmigrate"
	"github.com/enrichment/waterfall/internal/pgoutbox"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
	"github.com/enrichment/waterfall/internal/webhook"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Fail fast on a bad configuration, reporting EVERY problem at once, so a misconfigured
	// deploy is rejected at startup rather than erroring per-request.
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("invalid configuration; refusing to start", "errors", err.Error())
		os.Exit(1)
	}

	// Providers (mock; would be HTTPAdapters in production).
	adapters := []provider.Adapter{
		providertest.New("vendor-cheap", "jane.doe@acme.com", 0.72, 2, domain.FieldWorkEmail),
		providertest.New("vendor-premium", "jane.doe@acme.com", 0.80, 6, domain.FieldWorkEmail),
		providertest.New("vendor-phone", "+1-555-0100", 0.88, 5, domain.FieldMobilePhone),
	}
	reg := metrics.New()
	bnd := bandit.New() // Thompson-sampling router learner (ADR-0008), updated by the engine.

	// Datastore: Postgres (RLS-enforced tenant isolation + durable outbox) when POSTGRES_DSN is
	// set, else in-memory. With an admin DSN, migrations + app/relay roles are bootstrapped first
	// so a fresh cluster comes up ready; the app then connects as the NON-superuser app role so
	// RLS (G1) is actually enforced at runtime.
	pgDSN := cfg.PostgresDSN
	usePG := cfg.UsePostgres
	if usePG && cfg.PostgresAdminDSN != "" {
		if err := bootstrapPostgres(logger, cfg.PostgresAdminDSN); err != nil {
			logger.Error("postgres bootstrap failed", "err", err)
			os.Exit(1)
		}
	}

	var st store.Store
	var readyCheck func(context.Context) error
	if usePG {
		// Startup self-check: the app must connect as a role that does NOT bypass RLS (else G1 is
		// silently defeated), and the schema must already be migrated. Fail fast, loudly.
		if err := startupSelfCheck(pg.ParseDSN(pgDSN)); err != nil {
			logger.Error("postgres startup self-check failed", "err", err)
			os.Exit(1)
		}
		pgSt, err := pgstore.Open(pg.ParseDSN(pgDSN), 16)
		if err != nil {
			logger.Error("open pgstore", "err", err)
			os.Exit(1)
		}
		defer pgSt.Close()
		st = pgSt
		readyCheck = pgSt.Ping // /readyz is green only when the datastore is reachable
		logger.Info("postgres datastore ready (RLS-enforced app role verified)")
	} else {
		st = store.NewMemory()
	}
	eng := engine.New(st, adapters, engine.WithMetrics(reg), engine.WithBandit(bnd))

	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		// Build a per-request, bandit-scored plan. A fresh scorer per request avoids sharing
		// a non-concurrent RNG across workers, and the seed (derived from the record) makes a
		// given record's ordering reproducible on replay.
		seed := int64(fnv1a(req.JobID + "|" + req.Subject.ID))
		plan := router.New(adapters...).WithScorer(bnd.NewScorer(seed)).Plan(req)
		return eng.Run(ctx, req, plan)
	}

	queue := job.NewQueue(1024)
	reg.GaugeFunc("queue_depth", "Jobs currently queued across both lanes.", func() float64 {
		return float64(queue.Depth())
	})
	webhookDeliveries := reg.Counter("webhook_deliveries_total", "Webhook delivery attempts by result.", "result")

	// Job delivery, most durable first:
	//   POSTGRES_DSN -> transactional outbox in Postgres; a privileged relay claims pending rows
	//     (FOR UPDATE SKIP LOCKED) into the in-process queue and recovers in-flight jobs after a
	//     crash. This is the production path (survives process loss, not just a graceful stop).
	//   DURABLE_LOG  -> single-node file WAL outbox.
	//   else         -> in-process only (no crash safety).
	var jobs job.Store
	var submitter job.Submitter
	var deadLetters api.DeadLetterAdmin
	switch {
	case usePG:
		ob, err := pgoutbox.Open(pg.ParseDSN(pgDSN), 16)
		if err != nil {
			logger.Error("open pg outbox", "err", err)
			os.Exit(1)
		}
		defer ob.Close()
		// The relay reads/claims across tenants, so it uses a privileged (BYPASSRLS) connection;
		// job EXECUTION still runs under the job's captured principal, so writes stay tenant-scoped.
		relayDSN := cfg.PostgresRelayDSN
		if relayDSN == "" {
			relayDSN = pgDSN
		}
		relayConn, err := pg.Connect(pg.ParseDSN(relayDSN))
		if err != nil {
			logger.Error("connect pg relay", "err", err)
			os.Exit(1)
		}
		defer relayConn.Close()
		// Poison-job safety: cap redeliveries so a job that never reaches a terminal state
		// (e.g. crashes its worker every time) is parked in the dead-letter queue rather than
		// looping forever (validated config; default 10, tune with OUTBOX_MAX_ATTEMPTS).
		maxAttempts := cfg.OutboxMaxAttempts
		deadCtr := reg.Counter("outbox_dead_letter_total", "Jobs parked in the dead-letter queue after exceeding max delivery attempts.")
		// Short visibility timeout: a restarted relay re-claims jobs an old process had claimed
		// but not finished, so a crash mid-flight recovers in seconds rather than minutes.
		relay := pgoutbox.NewRelay(relayConn, queue, 3*time.Second,
			pgoutbox.WithMaxAttempts(maxAttempts),
			pgoutbox.WithDeadLetterHook(func(jobID string, attempts int) {
				deadCtr.Inc()
				logger.Warn("job dead-lettered (poison)", "job", jobID, "attempts", attempts)
			}),
		)
		relay.Start(200 * time.Millisecond)
		defer relay.Stop()
		jobs, submitter = ob, ob
		deadLetters = deadLetterAdapter{ob}
		logger.Info("postgres store + transactional outbox enabled", "max_attempts", maxAttempts)
	case cfg.DurableLog != "":
		p := cfg.DurableLog
		ds, err := durable.OpenStore(p)
		if err != nil {
			logger.Error("open durable log", "err", err)
			os.Exit(1)
		}
		defer ds.Close()
		relay := durable.NewRelay(ds, queue)
		relay.Start(100 * time.Millisecond) // recovers pending jobs on startup + drains
		defer relay.Stop()
		jobs, submitter = ds, ds
		logger.Info("durable delivery enabled", "log", p, "recovered_pending", len(ds.PendingOutbox()))
	default:
		mem := job.NewMemoryStore()
		jobs, submitter = mem, job.NewQueueSubmitter(mem, queue, nil)
	}

	// Webhook delivery: tenant-bound (URL only from the tenant's registered config) + through
	// the SSRF egress choke with a per-tenant allow-list. Inert unless a tenant is registered.
	webhookReg := webhook.MemoryRegistry{}
	if u := os.Getenv("WEBHOOK_ACME_URL"); u != "" {
		webhookReg["tenant-acme"] = webhook.Config{URL: u, Secret: os.Getenv("WEBHOOK_ACME_SECRET")}
	}
	webhookSender := webhook.NewSender(webhookReg, func(host string) *http.Client {
		return provider.NewEgressClient(provider.NewHostAllowList(host), provider.StaticKeyResolver{})
	})

	dispatcher := job.NewDispatcher(queue, jobs, run, job.WithOnComplete(func(ctx context.Context, j *job.Job) {
		if err := webhookSender.Deliver(ctx, j); err != nil {
			webhookDeliveries.Inc("failed")
			logger.Warn("webhook delivery failed", "job", j.ID, "err", err)
			return
		}
		webhookDeliveries.Inc("ok")
	}))
	dispatcher.Start(8) // worker pool
	defer dispatcher.Stop()

	// Worker heartbeat to the dashboard (OI-P5-1, doc 12 §P5). Fully OPT-IN: inert unless
	// DASH_HEARTBEAT_URL is set, so default enrichapi behavior is unchanged. The endpoint is
	// RBAC-gated, so the beat carries a machine JWT — either a pre-minted DASH_HEARTBEAT_JWT or one
	// minted here from DASH_HEARTBEAT_JWT_SECRET (HS256, role:operator + admin:write, short-lived).
	// The returned client also drives drain-gating (T5a/OI-P5-2): srv.ShouldClaim is wired to it
	// below, so a dashboard-set desired_state of draining/stopped makes this worker refuse NEW
	// submissions (503 draining + Retry-After) while in-flight jobs finish.
	var hbClient *heartbeat.Client
	if cfg.HeartbeatURL != "" {
		hb, hbStop := startHeartbeat(logger, cfg)
		hbClient = hb
		defer hbStop()
	}

	// Auth: verified JWT when JWT_HS256_SECRET is set (production path), else static dev
	// tokens. Either way, tenant_id flows ONLY from the authenticated principal (G1).
	var authr api.Authenticator
	writeScope := ""
	if cfg.UseJWT {
		v := auth.NewVerifier(
			auth.WithIssuer(cfg.JWTIssuer),
			auth.WithAudience(cfg.JWTAudience),
		)
		v.AddHMACKey(cfg.JWTKid, []byte(cfg.JWTSecret))
		authr = api.NewJWTAuthenticator(v)
		writeScope = "enrich:write" // writes require this JWT scope
		logger.Info("JWT auth enabled", "issuer", cfg.JWTIssuer, "audience", cfg.JWTAudience, "kid", cfg.JWTKid)
	} else {
		authr = api.NewStaticAuthenticator(map[string]tenant.Principal{
			// Dev tokens -> principals. Set JWT_HS256_SECRET to require verified JWTs.
			"acme-token":   {TenantID: "tenant-acme", UserID: "user-1", Scopes: []string{"enrich:write"}},
			"globex-token": {TenantID: "tenant-globex", UserID: "user-2", Scopes: []string{"enrich:write"}},
		})
		logger.Warn("using static dev tokens (set JWT_HS256_SECRET for verified JWT auth)")
	}

	srv := &api.Server{
		Auth:        authr,
		Limiter:     api.NewRateLimiter(50, 100, nil), // 50 rps/tenant, burst 100
		Dispatcher:  dispatcher,
		Submitter:   submitter,
		Jobs:        jobs,
		Records:     st,
		DeadLetters: deadLetters,
		Metrics:     reg,
		WriteScope:  writeScope,
		ReadyCheck:  readyCheck,
		Logger:      logger,
	}
	if hbClient != nil {
		// Drain-gating (T5a/OI-P5-2): only the heartbeat-echoed running state admits new work.
		srv.ShouldClaim = hbClient.ShouldClaim
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("enrichapi listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// bootstrapPostgres runs schema migrations and (idempotently) provisions the non-superuser app
// and relay roles over a privileged admin connection. It lets a fresh cluster come up ready
// while the app itself still connects as the least-privileged role, so RLS is enforced (G1).
// In production this is an ops step; here it makes the binary self-bootstrapping for a demo.
func bootstrapPostgres(logger *slog.Logger, adminDSN string) error {
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
	logger.Info("app/relay roles provisioned")
	return nil
}

// provisionRolesSQL creates the least-privileged app role (RLS-enforced) and the relay role
// (BYPASSRLS, for cross-tenant claim only) if absent, and grants exactly the table privileges
// each needs. Idempotent — safe to run on every startup. Requires a superuser admin connection
// (BYPASSRLS roles can only be created by a superuser).
const provisionRolesSQL = `
do $$
begin
  if not exists (select 1 from pg_roles where rolname = 'app_rls') then
    create role app_rls login nosuperuser;
  end if;
  if not exists (select 1 from pg_roles where rolname = 'relay') then
    create role relay login nosuperuser bypassrls;
  end if;
end
$$;
grant select, insert, update on field_versions, idempotency_ledger, cost_ledger, job_outbox to app_rls;
grant select, update on job_outbox to relay;
`

// startupSelfCheck verifies, over a fresh app-role connection, that the app can run safely:
//  1. the role does NOT bypass RLS (superuser/BYPASSRLS would silently defeat tenant isolation,
//     G1) — a common and dangerous misconfiguration;
//  2. the schema is migrated — the tables the app needs exist (a proxy the app role can read,
//     since it is not granted access to schema_migrations).
//
// Any failure returns an error so the caller refuses to start.
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
		return fmt.Errorf("app connects as role %q which bypasses row-level security (superuser=%v, bypassrls=%v); "+
			"tenant isolation (G1) would NOT be enforced — use a least-privileged app role", appCfg.User, super, bypassRLS)
	}

	var missing []string
	for _, tbl := range []string{"field_versions", "idempotency_ledger", "cost_ledger", "job_outbox"} {
		if err := c.Exec("select 1 from " + tbl + " where false"); err != nil {
			missing = append(missing, tbl)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tables missing (run migrations via POSTGRES_ADMIN_DSN or scripts): %v", missing)
	}
	return nil
}

// deadLetterAdapter adapts the Postgres outbox's DLQ operations to the api.DeadLetterAdmin
// interface, converting the store's row type to the API DTO. Keeps api and pgoutbox decoupled.
type deadLetterAdapter struct{ ob *pgoutbox.Store }

func (d deadLetterAdapter) DeadLetters(ctx context.Context, limit int) ([]api.DeadLetterItem, error) {
	rows, err := d.ob.DeadLetters(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.DeadLetterItem, len(rows))
	for i, r := range rows {
		out[i] = api.DeadLetterItem{
			JobID:     r.JobID,
			Status:    r.Status,
			Attempts:  r.Attempts,
			LastError: r.LastError,
			UpdatedAt: r.UpdatedAt,
		}
	}
	return out, nil
}

func (d deadLetterAdapter) Redrive(ctx context.Context, jobID string) (bool, error) {
	return d.ob.Redrive(ctx, jobID)
}

// fnv1a is a small stable hash used to seed the per-record bandit scorer, so a given
// record's routing order reproduces on replay.
func fnv1a(s string) uint32 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

// startHeartbeat wires the opt-in worker heartbeat client (OI-P5-1). It posts to the dashboard's
// RBAC-gated /v1/admin/workers/{id}/heartbeat and converges on the echoed desired_state. Returns
// the client (whose ShouldClaim drives the T5a drain gate on api.Server) and a
// stop func. The bearer is a pre-minted DASH_HEARTBEAT_JWT, or one minted here from
// DASH_HEARTBEAT_JWT_SECRET. jobs_active drain-gating of the dispatcher is a documented follow-up
// (OI-P5-2): this loop conveys liveness + desired_state; the client's ShouldClaim()/DesiredState()
// expose the drain signal for a future dispatcher gate.
func startHeartbeat(logger *slog.Logger, cfg *config.Config) (*heartbeat.Client, func()) {
	workerID := cfg.HeartbeatWorkerID
	if workerID == "" {
		host, _ := os.Hostname()
		workerID = host + "-" + strconv.Itoa(os.Getpid())
	}
	bearer := func() (string, error) {
		if cfg.HeartbeatJWT != "" {
			return cfg.HeartbeatJWT, nil
		}
		if cfg.HeartbeatJWTSecret == "" {
			return "", nil
		}
		return mintHS256(cfg), nil
	}
	transport := heartbeat.NewHTTPTransport(heartbeat.HTTPConfig{BaseURL: cfg.HeartbeatURL, Bearer: bearer})
	hb := heartbeat.New(heartbeat.Config{
		Transport: transport, WorkerID: workerID, Kind: orDefault(cfg.HeartbeatKind, "enrichd"),
		Region: cfg.HeartbeatRegion, Queue: cfg.HeartbeatQueue, Version: cfg.HeartbeatVersion, Logger: logger,
	})
	interval := time.Duration(cfg.HeartbeatIntervalS) * time.Second
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := hb.Run(ctx, interval); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("heartbeat loop stopped", "err", err)
		}
	}()
	logger.Info("worker heartbeat enabled", "url", cfg.HeartbeatURL, "worker_id", workerID, "interval", interval)
	return hb, cancel
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// mintHS256 mints a short-lived HS256 machine JWT the dashboard verifier accepts (tenant_id +
// role:operator/admin:write scopes, exp, optional iss/aud). Hand-rolled from stdlib so the worker
// binary needs no test-support import; mirrors the compact JWS the internal/auth verifier parses.
func mintHS256(cfg *config.Config) string {
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header, _ := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT", "kid": cfg.HeartbeatJWTKid})
	now := time.Now()
	claims := map[string]any{
		"tenant_id": cfg.HeartbeatTenant,
		"scopes":    []string{"role:operator", "admin:write"},
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	}
	if cfg.HeartbeatJWTIssuer != "" {
		claims["iss"] = cfg.HeartbeatJWTIssuer
	}
	if cfg.HeartbeatJWTAudience != "" {
		claims["aud"] = cfg.HeartbeatJWTAudience
	}
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	mac := hmac.New(sha256.New, []byte(cfg.HeartbeatJWTSecret))
	mac.Write([]byte(signing))
	return signing + "." + b64(mac.Sum(nil))
}
