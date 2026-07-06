// Package config loads and validates the enrichapi service configuration from the environment,
// failing fast with ALL problems at once (not just the first) so a misconfigured deploy is
// rejected at startup instead of erroring per-request. It is pure (env in, Config + error out)
// so it is fully unit-testable without touching the process environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/enrichment/waterfall/internal/pg"
)

// Config is the validated service configuration.
type Config struct {
	Port int

	UsePostgres      bool
	PostgresDSN      string
	PostgresAdminDSN string // optional; if set, migrations + roles are bootstrapped at startup
	PostgresRelayDSN string // optional; privileged (BYPASSRLS) relay connection; defaults to DSN
	DurableLog       string // file-WAL outbox path (used only when Postgres is off)

	OutboxMaxAttempts int

	UseJWT      bool
	JWTSecret   string
	JWTIssuer   string
	JWTAudience string
	JWTKid      string

	// Heartbeat is the OPT-IN worker control channel to the dashboard (doc 12 §P5). It is fully
	// inert unless HeartbeatURL is set, so existing enrichapi behavior is unchanged. When enabled,
	// the worker posts to POST {HeartbeatURL}/v1/admin/workers/{id}/heartbeat and converges on the
	// echoed desired_state. The endpoint is RBAC-gated, so a machine JWT is required: supply a
	// pre-minted HeartbeatJWT, or a HeartbeatJWTSecret (HS256) to mint a short-lived one at runtime.
	Heartbeat bool // derived: HeartbeatURL != ""

	HeartbeatURL         string
	HeartbeatJWT         string // pre-minted bearer token (attached verbatim)
	HeartbeatJWTSecret   string // HS256 shared secret to mint a machine token (role:operator)
	HeartbeatJWTKid      string // JWT header kid the dashboard verifier selects on; default "default"
	HeartbeatJWTIssuer   string // optional iss claim (must match the dashboard verifier if it pins one)
	HeartbeatJWTAudience string // optional aud claim (must match the dashboard verifier if it pins one)
	HeartbeatTenant      string // tenant_id claim; default "platform" (operator writes are platform-scoped)
	HeartbeatWorkerID    string // this worker's stable id (the {id} path segment); main derives one if empty
	HeartbeatKind        string
	HeartbeatRegion      string
	HeartbeatQueue       string
	HeartbeatVersion     string
	HeartbeatIntervalS   int // beat cadence in seconds; default 10
}

// Load reads configuration via getenv (usually os.Getenv) and validates it. It returns the
// populated Config and a joined error describing EVERY problem found, or nil if all is well.
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		Port: 8080, OutboxMaxAttempts: 10, JWTKid: "default",
		HeartbeatJWTKid: "default", HeartbeatTenant: "platform", HeartbeatIntervalS: 10,
	}
	var errs []error

	if v := getenv("PORT"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			errs = append(errs, fmt.Errorf("PORT %q is not a valid port (1..65535)", v))
		} else {
			c.Port = n
		}
	}

	c.PostgresDSN = getenv("POSTGRES_DSN")
	c.PostgresAdminDSN = getenv("POSTGRES_ADMIN_DSN")
	c.PostgresRelayDSN = getenv("POSTGRES_RELAY_DSN")
	c.DurableLog = getenv("DURABLE_LOG")
	c.UsePostgres = c.PostgresDSN != ""

	checkDSN := func(name, dsn string) {
		if dsn == "" {
			return
		}
		cfg := pg.ParseDSN(dsn)
		if cfg.User == "" || cfg.Database == "" {
			errs = append(errs, fmt.Errorf("%s must include user= and dbname= (got %q)", name, dsn))
		}
	}
	checkDSN("POSTGRES_DSN", c.PostgresDSN)
	checkDSN("POSTGRES_ADMIN_DSN", c.PostgresAdminDSN)
	checkDSN("POSTGRES_RELAY_DSN", c.PostgresRelayDSN)

	// Coherence: an admin/relay DSN without a primary DSN is a mistake (silently ignored today).
	if !c.UsePostgres {
		if c.PostgresAdminDSN != "" {
			errs = append(errs, errors.New("POSTGRES_ADMIN_DSN is set but POSTGRES_DSN is not"))
		}
		if c.PostgresRelayDSN != "" {
			errs = append(errs, errors.New("POSTGRES_RELAY_DSN is set but POSTGRES_DSN is not"))
		}
	}
	// Postgres and the file-WAL outbox are mutually exclusive delivery modes.
	if c.UsePostgres && c.DurableLog != "" {
		errs = append(errs, errors.New("POSTGRES_DSN and DURABLE_LOG are both set; they are mutually exclusive delivery modes"))
	}

	if v := getenv("OUTBOX_MAX_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("OUTBOX_MAX_ATTEMPTS %q must be an integer >= 1", v))
		} else {
			c.OutboxMaxAttempts = n
		}
	}

	c.JWTSecret = getenv("JWT_HS256_SECRET")
	c.UseJWT = c.JWTSecret != ""
	if c.UseJWT {
		if len(c.JWTSecret) < 16 {
			errs = append(errs, errors.New("JWT_HS256_SECRET must be at least 16 bytes"))
		}
		c.JWTIssuer = getenv("JWT_ISSUER")
		c.JWTAudience = getenv("JWT_AUDIENCE")
		if k := getenv("JWT_KID"); k != "" {
			c.JWTKid = k
		}
	}

	// Heartbeat control channel (opt-in): everything below is inert unless DASH_HEARTBEAT_URL is set.
	c.HeartbeatURL = getenv("DASH_HEARTBEAT_URL")
	c.Heartbeat = c.HeartbeatURL != ""
	c.HeartbeatJWT = getenv("DASH_HEARTBEAT_JWT")
	c.HeartbeatJWTSecret = getenv("DASH_HEARTBEAT_JWT_SECRET")
	if v := getenv("DASH_HEARTBEAT_JWT_KID"); v != "" {
		c.HeartbeatJWTKid = v
	}
	c.HeartbeatJWTIssuer = getenv("DASH_HEARTBEAT_JWT_ISSUER")
	c.HeartbeatJWTAudience = getenv("DASH_HEARTBEAT_JWT_AUDIENCE")
	if v := getenv("DASH_HEARTBEAT_TENANT"); v != "" {
		c.HeartbeatTenant = v
	}
	c.HeartbeatWorkerID = getenv("DASH_HEARTBEAT_WORKER_ID")
	c.HeartbeatKind = getenv("DASH_HEARTBEAT_KIND")
	c.HeartbeatRegion = getenv("DASH_HEARTBEAT_REGION")
	c.HeartbeatQueue = getenv("DASH_HEARTBEAT_QUEUE")
	c.HeartbeatVersion = getenv("DASH_HEARTBEAT_VERSION")
	if v := getenv("DASH_HEARTBEAT_INTERVAL_S"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("DASH_HEARTBEAT_INTERVAL_S %q must be an integer >= 1", v))
		} else {
			c.HeartbeatIntervalS = n
		}
	}
	if c.Heartbeat {
		if u, err := url.Parse(c.HeartbeatURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("DASH_HEARTBEAT_URL %q must be an absolute http(s) URL", c.HeartbeatURL))
		}
		// The endpoint is RBAC-gated, so a credential is mandatory: exactly one of a pre-minted
		// token or a minting secret.
		switch {
		case c.HeartbeatJWT == "" && c.HeartbeatJWTSecret == "":
			errs = append(errs, errors.New("DASH_HEARTBEAT_URL is set but neither DASH_HEARTBEAT_JWT nor DASH_HEARTBEAT_JWT_SECRET is provided"))
		case c.HeartbeatJWT != "" && c.HeartbeatJWTSecret != "":
			errs = append(errs, errors.New("DASH_HEARTBEAT_JWT and DASH_HEARTBEAT_JWT_SECRET are both set; provide exactly one"))
		}
		if c.HeartbeatJWTSecret != "" && len(c.HeartbeatJWTSecret) < 16 {
			errs = append(errs, errors.New("DASH_HEARTBEAT_JWT_SECRET must be at least 16 bytes"))
		}
	} else if c.HeartbeatJWT != "" || c.HeartbeatJWTSecret != "" {
		// A credential without the URL is a misconfiguration (silently unused today).
		errs = append(errs, errors.New("DASH_HEARTBEAT_JWT/DASH_HEARTBEAT_JWT_SECRET is set but DASH_HEARTBEAT_URL is not"))
	}

	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, nil
}
