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
	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/httpx"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgmigrate"
)

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
				if err := sessions.DeleteExpired(context.Background()); err != nil {
					logger.Warn("session reaper", "err", err)
				}
			}
		}
	}()
	defer close(reaperStop)

	handler := srv.Handler()
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
	port           int
	dsn            string
	adminDSN       string
	masterKey      string
	pepper         []byte
	issuer         string
	jwtSecret      string
	jwtIssuer      string
	jwtAudience    string
	jwtKid         string
	trustedProxies []*net.IPNet
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
