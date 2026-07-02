package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/api"
	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

type testEnv struct {
	handler    http.Handler
	email      *providertest.Fake
	dispatcher *job.Dispatcher
	reg        *metrics.Registry
}

func setup(t *testing.T, limiter *api.RateLimiter) *testEnv {
	t.Helper()
	email := providertest.New("acme", "jane@acme.com", 0.9, 5, domain.FieldWorkEmail)
	adapters := []provider.Adapter{email}
	st := store.NewMemory()
	eng := engine.New(st, adapters, engine.WithClock(func() time.Time { return time.Unix(1700000000, 0) }))
	planner := router.New(adapters...)
	run := func(ctx context.Context, req domain.EnrichmentRequest) (engine.Outcome, error) {
		return eng.Run(ctx, req, planner.Plan(req))
	}

	q := job.NewQueue(4)
	jobs := job.NewMemoryStore()
	d := job.NewDispatcher(q, jobs, run)
	d.Start(2)
	t.Cleanup(d.Stop)

	if limiter == nil {
		limiter = api.NewRateLimiter(10000, 10000, nil)
	}
	reg := metrics.New()
	srv := &api.Server{
		Auth: api.NewStaticAuthenticator(map[string]tenant.Principal{
			"tokA": {TenantID: "tenant-A", UserID: "uA"},
			"tokB": {TenantID: "tenant-B", UserID: "uB"},
		}),
		Limiter:    limiter,
		Dispatcher: d,
		Submitter:  job.NewQueueSubmitter(jobs, q, nil),
		Jobs:       jobs,
		Records:    st,
		Metrics:    reg,
	}
	return &testEnv{handler: srv.Handler(), email: email, dispatcher: d, reg: reg}
}

func body(subjectID string, ceiling int64, target float64, fields ...string) map[string]any {
	return map[string]any{
		"subject":           map[string]any{"id": subjectID, "known": map[string]string{"company_domain": "acme.com"}},
		"want":              fields,
		"confidence_target": target,
		"cost_ceiling":      ceiling,
		"config_version":    "v1",
	}
}

func do(h http.Handler, method, target, token, idem string, b any) *httptest.ResponseRecorder {
	var r io.Reader
	if b != nil {
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
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
	return rec
}

func decodeJob(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestSubmitSync_ReturnsOutcomeWithProvenance(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-1",
		body("person-1", 100, 0.85, "work_email"))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	m := decodeJob(t, rec)
	if m["status"] != "succeeded" {
		t.Fatalf("want succeeded, got %v", m["status"])
	}
	filled, _ := m["filled"].(map[string]any)
	we, _ := filled["work_email"].(map[string]any)
	if we["value"] != "jane@acme.com" {
		t.Fatalf("bad value: %v", we["value"])
	}
	if we["idempotency_key"] == "" || we["provider"] != "acme" {
		t.Fatalf("G5: incomplete provenance in response: %v", we)
	}
}

func TestSubmitAsync_ThenPoll(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-async",
		body("person-2", 100, 0.85, "work_email"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	id, _ := decodeJob(t, rec)["job_id"].(string)
	if id == "" {
		t.Fatal("no job_id returned")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		poll := do(env.handler, "GET", "/v1/enrichments/"+id, "tokA", "", nil)
		if poll.Code == http.StatusOK && decodeJob(t, poll)["status"] == "succeeded" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("async job did not complete: %s", poll.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAuthRequired(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments", "", "idem-x", body("p", 10, 0.8, "work_email"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without token, got %d", rec.Code)
	}
	bad := do(env.handler, "POST", "/v1/enrichments", "not-a-token", "idem-x", body("p", 10, 0.8, "work_email"))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 with bad token, got %d", bad.Code)
	}
}

func TestMissingIdempotencyKey(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "", body("p", 10, 0.8, "work_email"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 without Idempotency-Key, got %d", rec.Code)
	}
}

func TestValidation_UnknownField(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-v",
		body("p", 10, 0.8, "not_a_real_field"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for unknown field, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIdempotentReplay_NoSecondRun(t *testing.T) {
	env := setup(t, nil)
	b := body("person-3", 100, 0.99, "work_email") // unreachable target => 1 provider call
	first := do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-rep", b)
	second := do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-rep", b)

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("codes: %d, %d", first.Code, second.Code)
	}
	if id1, id2 := decodeJob(t, first)["job_id"], decodeJob(t, second)["job_id"]; id1 != id2 {
		t.Fatalf("same idempotency key must yield same job id: %v vs %v", id1, id2)
	}
	if env.email.Calls() != 1 {
		t.Fatalf("idempotent replay must not re-run the provider; calls=%d", env.email.Calls())
	}
}

func TestIdempotencyKeyReuse_Conflict(t *testing.T) {
	env := setup(t, nil)
	do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-dup", body("p-a", 100, 0.8, "work_email"))
	// Same key, different body.
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-dup", body("p-DIFFERENT", 100, 0.8, "work_email"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on key reuse with a different body, got %d", rec.Code)
	}
}

func TestCrossTenantJobGet_404(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-iso", body("p", 100, 0.8, "work_email"))
	id := decodeJob(t, rec)["job_id"].(string)
	// Tenant B tries to read tenant A's job.
	other := do(env.handler, "GET", "/v1/enrichments/"+id, "tokB", "", nil)
	if other.Code != http.StatusNotFound {
		t.Fatalf("G1: cross-tenant job read must be 404, got %d", other.Code)
	}
}

func TestRateLimit_429(t *testing.T) {
	env := setup(t, api.NewRateLimiter(0, 1, nil)) // burst 1, no refill
	first := do(env.handler, "GET", "/v1/records/x", "tokA", "", nil)
	if first.Code == http.StatusTooManyRequests {
		t.Fatal("first request should pass the burst")
	}
	second := do(env.handler, "GET", "/v1/records/x", "tokA", "", nil)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 after burst exhausted, got %d", second.Code)
	}
}

func TestGetRecord_AfterEnrich(t *testing.T) {
	env := setup(t, nil)
	do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-rec", body("person-rec", 100, 0.85, "work_email"))
	rec := do(env.handler, "GET", "/v1/records/person-rec", "tokA", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var resp struct {
		Fields map[string]struct {
			Value string `json:"value"`
		} `json:"fields"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Fields["work_email"].Value != "jane@acme.com" {
		t.Fatalf("record read missing enriched field: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "GET", "/healthz", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz should be 200, got %d", rec.Code)
	}
}
