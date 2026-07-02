package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/api"
	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/auth/authtest"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
)

// TestJWT_AuthAndScope proves the production auth path: a verified JWT binds the tenant
// principal (G1) and its scopes gate writes. tenant_id comes from the token, never the body.
func TestJWT_AuthAndScope(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	email := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	adapters := []provider.Adapter{email}
	st := store.NewMemory()
	eng := engine.New(st, adapters, engine.WithClock(func() time.Time { return now }))
	planner := router.New(adapters...)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}
	q := job.NewQueue(4)
	jobs := job.NewMemoryStore()
	d := job.NewDispatcher(q, jobs, run)
	d.Start(2)
	t.Cleanup(d.Stop)

	secret := []byte("api-hs256-secret-key-for-tests-000000")
	v := auth.NewVerifier(
		auth.WithIssuer("https://issuer.example"),
		auth.WithAudience("enrichment-api"),
		auth.WithClock(func() time.Time { return now }),
	)
	v.AddHMACKey("k1", secret)

	srv := &api.Server{
		Auth:       api.NewJWTAuthenticator(v),
		Limiter:    api.NewRateLimiter(10000, 10000, nil),
		Dispatcher: d,
		Submitter:  job.NewQueueSubmitter(jobs, q, nil),
		Jobs:       jobs,
		Records:    st,
		WriteScope: "enrich:write",
		Now:        func() time.Time { return now },
	}
	h := srv.Handler()

	mk := func(tenantID, scope string, exp int64) string {
		return authtest.SignHS256(secret, "k1", map[string]any{
			"sub": "u1", "iss": "https://issuer.example", "aud": "enrichment-api",
			"exp": exp, "tenant_id": tenantID, "scope": scope,
		})
	}
	future := now.Add(time.Hour).Unix()
	past := now.Add(-time.Hour).Unix()

	// valid token WITH the write scope -> accepted
	rec := do(h, "POST", "/v1/enrichments", mk("tenant-A", "enrich:write enrich:read", future), "idem-1",
		body("p1", 100, 0.8, "work_email"))
	if rec.Code != 200 && rec.Code != 202 {
		t.Fatalf("valid JWT + scope should be accepted, got %d: %s", rec.Code, rec.Body.String())
	}

	// valid token WITHOUT the write scope -> 403 Forbidden (authenticated but not authorized)
	rec = do(h, "POST", "/v1/enrichments", mk("tenant-A", "enrich:read", future), "idem-2",
		body("p1", 100, 0.8, "work_email"))
	if rec.Code != 403 {
		t.Fatalf("missing scope should be 403, got %d", rec.Code)
	}

	// expired token -> 401
	rec = do(h, "POST", "/v1/enrichments", mk("tenant-A", "enrich:write", past), "idem-3",
		body("p1", 100, 0.8, "work_email"))
	if rec.Code != 401 {
		t.Fatalf("expired token should be 401, got %d", rec.Code)
	}

	// no token -> 401
	rec = do(h, "POST", "/v1/enrichments", "", "idem-4", body("p1", 100, 0.8, "work_email"))
	if rec.Code != 401 {
		t.Fatalf("no token should be 401, got %d", rec.Code)
	}

	// garbage token -> 401 (verification fails, no info leak)
	rec = do(h, "POST", "/v1/enrichments", "not.a.jwt", "idem-5", body("p1", 100, 0.8, "work_email"))
	if rec.Code != 401 {
		t.Fatalf("garbage token should be 401, got %d", rec.Code)
	}
}
