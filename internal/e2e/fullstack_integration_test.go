//go:build integration

// Package e2e_test is a black-box, full-stack integration test: a real signed JWT drives the
// HTTP gateway, through the async queue + worker pool, into the Execution Engine, whose G5
// store is LIVE PostgreSQL with row-level security, and out to a signed webhook. It asserts
// the correctness gates hold across the wired system — not in isolation:
//
//	G1 tenant isolation (live RLS)   G2 idempotency (no double paid call)
//	G4 cost ceiling (no overspend)   G5 provenance on every value
//
// Run via scripts/run-rls-test.sh or with WATERFALL_PG_DSN set:
//
//	WATERFALL_PG_DSN="host=127.0.0.1 port=55432 user=postgres dbname=postgres" \
//	  go test -tags integration ./internal/e2e/ -run TestE2E_FullStack -v
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/api"
	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/auth/authtest"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/pgstore"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/webhook"
)

type fieldResp struct {
	Value          string  `json:"value"`
	Confidence     float64 `json:"confidence"`
	Provider       string  `json:"provider"`
	CostCredits    int64   `json:"cost_credits"`
	IdempotencyKey string  `json:"idempotency_key"`
	ObservedAt     string  `json:"observed_at"`
}

type jobResp struct {
	JobID     string               `json:"job_id"`
	Status    string               `json:"status"`
	Committed int64                `json:"committed_credits"`
	Filled    map[string]fieldResp `json:"filled"`
	Error     string               `json:"error"`
}

type recResp struct {
	SubjectID string               `json:"subject_id"`
	Fields    map[string]fieldResp `json:"fields"`
}

type webhookHit struct {
	sig   string
	event string
	body  []byte
}

func TestE2E_FullStack(t *testing.T) {
	dsn := os.Getenv("WATERFALL_PG_DSN")
	if dsn == "" {
		t.Skip("set WATERFALL_PG_DSN to run the full-stack E2E test")
	}
	cfg := pg.ParseDSN(dsn)

	// --- schema + non-superuser role (RLS applies to it) ---
	admin, err := pg.Connect(cfg)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()
	setupSchema(t, admin)

	appCfg := cfg
	appCfg.User = "app_rls"
	// The FULL store is Postgres: G5 (field_versions), G2 (idempotency_ledger), and G4
	// (cost_ledger) are ALL RLS-scoped datastore tables now — nothing in memory.
	st, err := pgstore.Open(appCfg, 8)
	if err != nil {
		t.Fatalf("open pgstore: %v", err)
	}
	defer st.Close()

	// --- engine + providers (deterministic fakes that count calls) ---
	cheap := providertest.New("vendor-cheap", "jane@acme.com", 0.72, 2, domain.FieldWorkEmail)
	premium := providertest.New("vendor-premium", "jane@acme.com", 0.80, 6, domain.FieldWorkEmail)
	adapters := []provider.Adapter{cheap, premium}
	now := time.Unix(1_700_000_000, 0)
	eng := engine.New(st, adapters, engine.WithClock(func() time.Time { return now }))
	planner := router.New(adapters...)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}

	// --- webhook sink (loopback). The production egress guard blocks loopback BY DESIGN
	// (SSRF, unit-tested in Slice 05); here we use a plain client to exercise the
	// delivery/signing/tenant-binding path end-to-end. ---
	var mu sync.Mutex
	var hits []webhookHit
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, webhookHit{sig: r.Header.Get("X-Waterfall-Signature"), event: r.Header.Get("X-Waterfall-Event"), body: b})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	const whSecret = "wh-secret-e2e"
	reg := webhook.MemoryRegistry{"tenant-e2e": {URL: sink.URL, Secret: whSecret}}
	sender := webhook.NewSender(reg, func(host string) *http.Client { return &http.Client{Timeout: 2 * time.Second} })

	// --- queue + dispatcher (webhook on completion) ---
	q := job.NewQueue(16)
	jobs := job.NewMemoryStore()
	dispatcher := job.NewDispatcher(q, jobs, run, job.WithOnComplete(func(ctx context.Context, j *job.Job) {
		_ = sender.Deliver(ctx, j)
	}))
	dispatcher.Start(4)
	defer dispatcher.Stop()
	submitter := job.NewQueueSubmitter(jobs, q, nil)

	// --- real JWT auth ---
	secret := []byte("e2e-hs256-secret-key-0000000000000000")
	v := auth.NewVerifier(auth.WithIssuer("iss"), auth.WithAudience("aud"), auth.WithClock(func() time.Time { return now }))
	v.AddHMACKey("k1", secret)
	token := func(tenantID string) string {
		return authtest.SignHS256(secret, "k1", map[string]any{
			"sub": "u", "iss": "iss", "aud": "aud",
			"exp": now.Add(time.Hour).Unix(), "tenant_id": tenantID, "scope": "enrich:write",
		})
	}

	srv := &api.Server{
		Auth:       api.NewJWTAuthenticator(v),
		Limiter:    api.NewRateLimiter(100000, 100000, nil),
		Dispatcher: dispatcher,
		Submitter:  submitter,
		Jobs:       jobs,
		Records:    st, // GET /records reads from live Postgres (RLS-scoped)
		WriteScope: "enrich:write",
		Now:        func() time.Time { return now },
	}
	h := srv.Handler()

	emailReq := map[string]any{
		"subject":           map[string]any{"id": "subj-1", "known": map[string]string{"company_domain": "acme.com"}},
		"want":              []string{"work_email"},
		"confidence_target": 0.85,
		"cost_ceiling":      20,
		"config_version":    "v1",
	}

	// ==== job 1 (tenant-e2e) ====
	id1 := submitAsync(t, h, token("tenant-e2e"), "idem-1", emailReq)
	waitDone(t, h, token("tenant-e2e"), id1)

	// G5: the value carries full provenance, read back from Postgres.
	rec := getRecord(t, h, token("tenant-e2e"), "subj-1")
	we, ok := rec.Fields["work_email"]
	if !ok || we.Value != "jane@acme.com" {
		t.Fatalf("expected filled work_email, got %+v", rec.Fields)
	}
	if we.Provider == "" || we.IdempotencyKey == "" || we.Confidence <= 0 {
		t.Fatalf("G5 provenance missing: %+v", we)
	}

	// G1 (LIVE RLS): a different tenant sees nothing for subj-1.
	recOther := getRecord(t, h, token("tenant-other"), "subj-1")
	if len(recOther.Fields) != 0 {
		t.Fatalf("G1 breach: tenant-other saw %d fields for subj-1", len(recOther.Fields))
	}

	// G2: a SECOND job (different idempotency key) for the SAME record+field+params must
	// serve from the idempotency ledger — no new paid provider call.
	callsBefore := cheap.Calls() + premium.Calls()
	id2 := submitAsync(t, h, token("tenant-e2e"), "idem-2", emailReq)
	waitDone(t, h, token("tenant-e2e"), id2)
	if after := cheap.Calls() + premium.Calls(); after != callsBefore {
		t.Fatalf("G2 breach: identical request triggered %d new provider calls", after-callsBefore)
	}

	// G4: a job with a tight ceiling must not overspend.
	tightReq := map[string]any{
		"subject":           map[string]any{"id": "subj-2", "known": map[string]string{"company_domain": "acme.com"}},
		"want":              []string{"work_email"},
		"confidence_target": 0.99, // wants premium too...
		"cost_ceiling":      2,    // ...but only the cheap (cost 2) provider fits
		"config_version":    "v1",
	}
	id3 := submitAsync(t, h, token("tenant-e2e"), "idem-3", tightReq)
	final := waitDone(t, h, token("tenant-e2e"), id3)
	if final.Committed > 2 {
		t.Fatalf("G4 breach: committed %d exceeds ceiling 2", final.Committed)
	}

	// Webhook: a signed completion callback was delivered for the tenant (poll: OnComplete
	// runs just after the job flips to succeeded).
	if !waitForVerifiedWebhook(&mu, &hits, whSecret) {
		t.Fatal("expected at least one webhook delivery with a valid signature")
	}

	// Datastore durability: the G2 and G4 ledgers actually persisted to Postgres (checked as
	// superuser, which sees all tenants' rows).
	assertNonZero(t, admin, "select count(*) from idempotency_ledger")
	assertNonZero(t, admin, "select count(*) from cost_ledger")
}

// --- helpers ---

func setupSchema(t *testing.T, admin *pg.Conn) {
	t.Helper()
	for _, s := range []string{
		"drop owned by app_rls cascade",
		"drop role if exists app_rls",
		"drop table if exists field_versions, idempotency_ledger, cost_ledger cascade",
		"drop function if exists app_current_tenant() cascade",
	} {
		_ = admin.Exec(s)
	}
	ddl, err := os.ReadFile("../../migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if err := admin.Exec(string(ddl)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	if err := admin.Exec("create role app_rls login nosuperuser"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	// UPDATE is needed for the cost-ledger reservation (G4); SELECT/INSERT for the rest.
	if err := admin.Exec("grant select, insert, update on field_versions, idempotency_ledger, cost_ledger to app_rls"); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

func assertNonZero(t *testing.T, c *pg.Conn, sql string) {
	t.Helper()
	res, err := c.Query(sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil || *res.Rows[0][0] == "0" {
		t.Fatalf("expected a non-zero count for %q", sql)
	}
}

func doReq(h http.Handler, method, target, token, idem string, body any) (int, []byte) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func submitAsync(t *testing.T, h http.Handler, token, idem string, body any) string {
	t.Helper()
	code, resp := doReq(h, "POST", "/v1/enrichments", token, idem, body)
	if code != http.StatusAccepted && code != http.StatusOK {
		t.Fatalf("submit expected 202/200, got %d: %s", code, resp)
	}
	var jr jobResp
	if err := json.Unmarshal(resp, &jr); err != nil {
		t.Fatalf("decode submit resp: %v", err)
	}
	if jr.JobID == "" {
		t.Fatalf("no job id in response: %s", resp)
	}
	return jr.JobID
}

func waitDone(t *testing.T, h http.Handler, token, id string) jobResp {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, resp := doReq(h, "GET", "/v1/enrichments/"+id, token, "", nil)
		if code == http.StatusOK {
			var jr jobResp
			if err := json.Unmarshal(resp, &jr); err == nil {
				if jr.Status == "succeeded" || jr.Status == "failed" {
					if jr.Status == "failed" {
						t.Fatalf("job %s failed: %s", id, jr.Error)
					}
					return jr
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish in time", id)
	return jobResp{}
}

func getRecord(t *testing.T, h http.Handler, token, subjectID string) recResp {
	t.Helper()
	code, resp := doReq(h, "GET", "/v1/records/"+subjectID, token, "", nil)
	if code != http.StatusOK {
		t.Fatalf("get record expected 200, got %d: %s", code, resp)
	}
	var rr recResp
	if err := json.Unmarshal(resp, &rr); err != nil {
		t.Fatalf("decode record resp: %v", err)
	}
	return rr
}

func waitForVerifiedWebhook(mu *sync.Mutex, hits *[]webhookHit, secret string) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, hit := range *hits {
			if hit.event != "" && webhook.Verify(secret, hit.body, hit.sig) {
				mu.Unlock()
				return true
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
