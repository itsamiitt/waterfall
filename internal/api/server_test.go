package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
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

// setupDrain builds an env whose Server's ShouldClaim is backed by a caller-toggleable flag, so a
// test can flip the worker between claiming and draining and observe admission decisions.
func setupDrain(t *testing.T, claim *atomic.Bool) *testEnv {
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
	srv := &api.Server{
		Auth: api.NewStaticAuthenticator(map[string]tenant.Principal{
			"tokA": {TenantID: "tenant-A", UserID: "uA"},
		}),
		Limiter:     api.NewRateLimiter(10000, 10000, nil),
		Dispatcher:  d,
		Submitter:   job.NewQueueSubmitter(jobs, q, nil),
		Jobs:        jobs,
		Records:     st,
		ShouldClaim: func() bool { return claim.Load() },
	}
	return &testEnv{handler: srv.Handler(), email: email, dispatcher: d}
}

// TestDrainGate_503WhenDraining is the T5a/OI-P5-2 acceptance: with ShouldClaim()==false a new
// submission is refused 503 {"error":{"code":"draining"}} + Retry-After; flipping ShouldClaim back
// to true admits again; and reads (GET) plus in-flight jobs are never gated.
func TestDrainGate_503WhenDraining(t *testing.T) {
	var claim atomic.Bool
	claim.Store(true)
	env := setupDrain(t, &claim)

	// A job admitted while claiming stays retrievable even after we start draining (in-flight
	// unaffected — the gate only blocks NEW admissions).
	first := do(env.handler, "POST", "/v1/enrichments?mode=sync", "tokA", "idem-live", body("p-live", 100, 0.85, "work_email"))
	if first.Code != http.StatusOK {
		t.Fatalf("want 200 while claiming, got %d: %s", first.Code, first.Body.String())
	}
	liveID := decodeJob(t, first)["job_id"].(string)

	// Flip to draining: new submits (sync and async) are refused 503 draining + Retry-After.
	claim.Store(false)
	for _, target := range []string{"/v1/enrichments", "/v1/enrichments?mode=sync"} {
		rec := do(env.handler, "POST", target, "tokA", "idem-drain", body("p-drain", 100, 0.85, "work_email"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s while draining: want 503, got %d", target, rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: draining 503 must set Retry-After", target)
		}
		if code := decodeJob(t, rec)["error"].(map[string]any)["code"]; code != "draining" {
			t.Fatalf("%s: error code = %v, want draining", target, code)
		}
	}

	// Reads are never gated: the in-flight/committed job is still retrievable while draining.
	poll := do(env.handler, "GET", "/v1/enrichments/"+liveID, "tokA", "", nil)
	if poll.Code != http.StatusOK {
		t.Fatalf("read while draining must not be gated, got %d", poll.Code)
	}

	// Resume claiming: admissions succeed again.
	claim.Store(true)
	again := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-again", body("p-again", 100, 0.85, "work_email"))
	if again.Code != http.StatusAccepted {
		t.Fatalf("want 202 after resuming, got %d: %s", again.Code, again.Body.String())
	}
}

// TestDrainGate_NilShouldClaimAlwaysAdmits pins backward compatibility: a Server with no ShouldClaim
// wired (the default) never drains.
func TestDrainGate_NilShouldClaimAlwaysAdmits(t *testing.T) {
	env := setup(t, nil) // no ShouldClaim
	rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-nil", body("p", 100, 0.85, "work_email"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("nil ShouldClaim must always admit; got %d", rec.Code)
	}
}

// TestDrainGate_ConcurrentToggle exercises the gate under a racing ShouldClaim flip (run with -race):
// every response is a well-formed admit (200/202) or a draining refusal (503), never a panic or a
// torn state.
func TestDrainGate_ConcurrentToggle(t *testing.T) {
	var claim atomic.Bool
	claim.Store(true)
	env := setupDrain(t, &claim)

	// A toggler flips the drain state under the submitters (separate from the submitter WaitGroup so
	// it can run until they finish).
	stop := make(chan struct{})
	toggled := make(chan struct{})
	go func() {
		defer close(toggled)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				claim.Store(i%2 == 0)
			}
		}
	}()

	var wg sync.WaitGroup
	var errs atomic.Int64
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := do(env.handler, "POST", "/v1/enrichments", "tokA", "idem-conc-"+strconv.Itoa(i),
				body("p-conc", 100, 0.85, "work_email"))
			switch rec.Code {
			// Admitted (202), drained (503), or a well-formed async shed (429 queue_full): all are
			// valid, torn-state-free outcomes. The gate must never panic or produce anything else.
			case http.StatusAccepted, http.StatusServiceUnavailable, http.StatusTooManyRequests:
			default:
				errs.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(stop)
	<-toggled
	if errs.Load() != 0 {
		t.Fatalf("got %d responses that were neither 202 nor 503", errs.Load())
	}
}

func TestHealthz(t *testing.T) {
	env := setup(t, nil)
	rec := do(env.handler, "GET", "/healthz", "", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz should be 200, got %d", rec.Code)
	}
}
