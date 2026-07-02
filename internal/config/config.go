// Package config loads and validates the enrichapi service configuration from the environment,
// failing fast with ALL problems at once (not just the first) so a misconfigured deploy is
// rejected at startup instead of erroring per-request. It is pure (env in, Config + error out)
// so it is fully unit-testable without touching the process environment.
package config

import (
	"errors"
	"fmt"
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
}

// Load reads configuration via getenv (usually os.Getenv) and validates it. It returns the
// populated Config and a joined error describing EVERY problem found, or nil if all is well.
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{Port: 8080, OutboxMaxAttempts: 10, JWTKid: "default"}
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

	if len(errs) > 0 {
		return c, errors.Join(errs...)
	}
	return c, nil
}
