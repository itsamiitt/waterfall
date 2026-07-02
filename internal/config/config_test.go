package config

import (
	"strings"
	"testing"
)

// env builds a getenv function from a map.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_Defaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatalf("empty env should be valid: %v", err)
	}
	if c.Port != 8080 || c.OutboxMaxAttempts != 10 || c.JWTKid != "default" {
		t.Fatalf("defaults wrong: %+v", c)
	}
	if c.UsePostgres || c.UseJWT {
		t.Fatalf("nothing should be enabled by default: %+v", c)
	}
}

func TestLoad_ValidPostgresAndJWT(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PORT":                "9090",
		"POSTGRES_DSN":        "host=db port=5432 user=app_rls dbname=waterfall",
		"POSTGRES_ADMIN_DSN":  "host=db user=postgres dbname=waterfall",
		"OUTBOX_MAX_ATTEMPTS": "5",
		"JWT_HS256_SECRET":    "a-sufficiently-long-secret",
		"JWT_ISSUER":          "https://issuer",
		"JWT_KID":             "k1",
	}))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if c.Port != 9090 || !c.UsePostgres || c.OutboxMaxAttempts != 5 || !c.UseJWT || c.JWTKid != "k1" {
		t.Fatalf("parsed wrong: %+v", c)
	}
}

func TestLoad_AggregatesAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"PORT":                "70000",   // out of range
		"POSTGRES_DSN":        "host=db", // missing user + dbname
		"OUTBOX_MAX_ATTEMPTS": "0",       // < 1
		"JWT_HS256_SECRET":    "short",   // < 16 bytes
	}))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{"PORT", "POSTGRES_DSN", "OUTBOX_MAX_ATTEMPTS", "JWT_HS256_SECRET"} {
		if !strings.Contains(msg, want) {
			t.Errorf("aggregated error should mention %s; got:\n%s", want, msg)
		}
	}
}

func TestLoad_CoherenceChecks(t *testing.T) {
	// admin/relay DSN without a primary DSN.
	if _, err := Load(env(map[string]string{"POSTGRES_ADMIN_DSN": "host=db user=postgres dbname=w"})); err == nil ||
		!strings.Contains(err.Error(), "POSTGRES_ADMIN_DSN is set but POSTGRES_DSN is not") {
		t.Fatalf("expected admin-without-primary error, got %v", err)
	}
	// Postgres + file-WAL both set.
	if _, err := Load(env(map[string]string{
		"POSTGRES_DSN": "host=db user=app dbname=w",
		"DURABLE_LOG":  "/tmp/wal",
	})); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}
