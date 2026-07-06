// Command dashseed seeds a customer Tenant + an MFA-enrolled operator User for the live
// Playwright E2E (OI-P12-2). It uses the SAME secrets backend + master key as dashboardd, so the
// sealed TOTP seed round-trips; it prints the base32 seed the spec turns into TOTP codes. Dev-only
// tooling — never part of a production deploy.
//
// PRODUCTION path (SEC-3, ADR-0021): dashseed is superseded by the audited operator provisioning
// API — POST /v1/admin/tenants creates the Tenant + first tenant_admin + one-time invite token
// under RBAC + MFA step-up, and the public POST /v1/admin/auth/accept-invite sets the first
// password. Use this binary only for local dev/E2E seeding where no operator session exists yet.
package main

import (
	"context"
	"encoding/base32"
	"fmt"
	"os"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	master := os.Getenv("DASH_MASTER_KEY")
	pepper := os.Getenv("DASH_FINGERPRINT_PEPPER")
	email := envOr("SEED_EMAIL", "ops@acme.example")
	password := envOr("SEED_PASSWORD", "correct horse battery staple")
	tenantID := envOr("SEED_TENANT", "acme")

	pool := pg.NewPool(pg.ParseDSN(dsn), 4)
	defer pool.Close()
	store := db.New(pool)
	kr, err := secrets.NewKeyring(master)
	must(err)
	backend := secrets.NewPGBackend(store, kr, []byte(pepper))
	users := security.NewUsers(store, backend, "Waterfall")

	// Create the customer Tenant via the superuser admin connection — tenant lifecycle writes are
	// deliberately outside the RLS app-role surface (doc 05 SEC-3: operators read all Tenants but
	// customer-Tenant provisioning happens via the signup/provisioning path). This dev seeder stands
	// in for that path.
	adminConn, err := pg.Connect(pg.ParseDSN(os.Getenv("POSTGRES_ADMIN_DSN")))
	must(err)
	must(adminConn.ExecParams(`insert into tenants (id, name, kind, status) values ($1,$2,'customer','active') on conflict (id) do nothing`, tenantID, tenantID))
	adminConn.Close()

	adminCtx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "seed", Scopes: []string{"role:tenant_admin"}})
	pwHash, err := security.HashPassword(password)
	must(err)
	uid, err := users.Create(adminCtx, email, pwHash, "operator")
	must(err)
	seed, _, err := users.EnrollMFA(adminCtx, uid, email)
	must(err)
	_, err = users.ConfirmMFA(adminCtx, uid, security.GenerateTOTP(seed, time.Now()), time.Now())
	must(err)

	fmt.Printf("SEED_OK email=%s tenant=%s user_id=%s totp_base32=%s\n",
		email, tenantID, uid, base32.StdEncoding.EncodeToString(seed))
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dashseed:", err)
		os.Exit(1)
	}
}
