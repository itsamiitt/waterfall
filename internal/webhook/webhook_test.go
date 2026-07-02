package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/job"
	"github.com/enrichment/waterfall/internal/webhook"
)

func succeededJob(tenant, id string) *job.Job {
	out := engine.Outcome{
		Committed: 8,
		Filled: map[domain.Field]domain.FieldValue{
			domain.FieldWorkEmail: {
				Field: domain.FieldWorkEmail, Value: "jane@acme.com", Confidence: 0.9,
				Prov: domain.Provenance{Provider: "acme", ObservedAt: time.Unix(1700000000, 0), CostCredits: 8, IdempotencyKey: "k1"},
			},
		},
		Stops: map[domain.Field]engine.StopReason{},
	}
	return &job.Job{ID: id, TenantID: tenant, Status: job.StatusSucceeded, Outcome: &out}
}

func plainClient(string) *http.Client { return &http.Client{} }

func TestSignVerify(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	sig := webhook.Sign("secret", body)
	if !webhook.Verify("secret", body, sig) {
		t.Fatal("valid signature must verify")
	}
	if webhook.Verify("wrong", body, sig) {
		t.Fatal("wrong secret must not verify")
	}
	if webhook.Verify("secret", []byte("tampered"), sig) {
		t.Fatal("tampered body must not verify")
	}
}

func TestDeliver_SignsAndPosts(t *testing.T) {
	var gotSig, gotEvent string
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Waterfall-Signature")
		gotEvent = r.Header.Get("X-Waterfall-Event")
		received <- b
		w.WriteHeader(200)
	}))
	defer srv.Close()

	reg := webhook.MemoryRegistry{"tenant-A": {URL: srv.URL, Secret: "s3cr3t"}}
	s := webhook.NewSender(reg, plainClient, webhook.WithBackoff(0))

	if err := s.Deliver(context.Background(), succeededJob("tenant-A", "job1")); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	body := <-received
	if !webhook.Verify("s3cr3t", body, gotSig) {
		t.Fatal("receiver could not verify the HMAC signature")
	}
	if gotEvent != "enrichment.completed" {
		t.Fatalf("event header: %q", gotEvent)
	}
	var p map[string]any
	_ = json.Unmarshal(body, &p)
	if p["job_id"] != "job1" || p["status"] != "succeeded" {
		t.Fatalf("payload wrong: %s", body)
	}
}

// TestDeliver_TenantBound proves the anti-cross-tenant-egress guarantee: tenant A's result
// is delivered only to tenant A's registered endpoint, never tenant B's.
func TestDeliver_TenantBound(t *testing.T) {
	var aHits, bHits int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&aHits, 1)
		w.WriteHeader(200)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&bHits, 1)
		w.WriteHeader(200)
	}))
	defer srvB.Close()

	reg := webhook.MemoryRegistry{
		"tenant-A": {URL: srvA.URL, Secret: "sa"},
		"tenant-B": {URL: srvB.URL, Secret: "sb"},
	}
	s := webhook.NewSender(reg, plainClient, webhook.WithBackoff(0))

	if err := s.Deliver(context.Background(), succeededJob("tenant-A", "jA")); err != nil {
		t.Fatalf("deliver A: %v", err)
	}
	if atomic.LoadInt32(&aHits) != 1 {
		t.Fatalf("tenant A's endpoint should receive exactly 1 delivery, got %d", aHits)
	}
	if atomic.LoadInt32(&bHits) != 0 {
		t.Fatalf("G1 VIOLATION: tenant A's result reached tenant B's endpoint (%d hits)", bHits)
	}
}

func TestDeliver_SkipsWhenUnconfigured(t *testing.T) {
	var factoryCalls int32
	s := webhook.NewSender(webhook.MemoryRegistry{}, func(string) *http.Client {
		atomic.AddInt32(&factoryCalls, 1)
		return &http.Client{}
	}, webhook.WithBackoff(0))

	if err := s.Deliver(context.Background(), succeededJob("no-webhook", "j")); err != nil {
		t.Fatalf("unconfigured tenant should be a no-op, got %v", err)
	}
	if factoryCalls != 0 {
		t.Fatal("no HTTP client should be built when no webhook is configured")
	}
}

func TestDeliver_BoundedRetriesOn5xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	reg := webhook.MemoryRegistry{"t": {URL: srv.URL, Secret: "s"}}
	s := webhook.NewSender(reg, plainClient, webhook.WithMaxAttempts(3), webhook.WithBackoff(0))
	if err := s.Deliver(context.Background(), succeededJob("t", "j")); err == nil {
		t.Fatal("persistent 5xx should return an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("5xx should be retried up to maxAttempts=3, got %d", got)
	}
}

func TestDeliver_4xxIsTerminal(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(400)
	}))
	defer srv.Close()

	reg := webhook.MemoryRegistry{"t": {URL: srv.URL, Secret: "s"}}
	s := webhook.NewSender(reg, plainClient, webhook.WithMaxAttempts(3), webhook.WithBackoff(0))
	if err := s.Deliver(context.Background(), succeededJob("t", "j")); err == nil {
		t.Fatal("4xx should return an error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("4xx (non-429) must not be retried, got %d attempts", got)
	}
}
