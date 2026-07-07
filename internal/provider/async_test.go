package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// TestAsyncHTTPAdapter_SubmitPoll proves ADR-0024 Phase 3: the adapter submits a job, extracts a
// poll token, polls until the status is terminal (pending → done), and decodes the final result —
// with the egress key injected on every round-trip and the poll loop honouring the budget.
func TestAsyncHTTPAdapter_SubmitPoll(t *testing.T) {
	var pollCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("submit method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("X-API-Key"); got != "SECRET" {
			t.Errorf("submit missing injected key, got %q", got)
		}
		_, _ = w.Write([]byte(`{"job_id":"j1"}`))
	})
	mux.HandleFunc("/jobs/j1", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "SECRET" {
			t.Errorf("poll missing injected key, got %q", got)
		}
		pollCount++
		if pollCount < 2 {
			_, _ = w.Write([]byte(`{"status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"done","email":"jane@acme.com"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"async:default": "SECRET"})

	a := &provider.AsyncHTTPAdapter{
		NameV:        "async",
		BaseURL:      srv.URL,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-API-Key", KeyPoolSelector: "async:default"},
		Caps:         []provider.Capability{{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.9}},
		PollInterval: 5 * time.Millisecond,
		Submit: func(ctx context.Context, base string, _ provider.Request) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, base+"/submit", strings.NewReader("{}"))
		},
		ParseSubmit: func(body []byte) (string, error) {
			var s struct {
				JobID string `json:"job_id"`
			}
			if err := json.Unmarshal(body, &s); err != nil {
				return "", err
			}
			return s.JobID, nil
		},
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/jobs/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			var s struct {
				Status string `json:"status"`
				Email  string `json:"email"`
			}
			if err := json.Unmarshal(body, &s); err != nil {
				return provider.Result{}, false, err
			}
			if s.Status != "done" {
				return provider.Result{}, false, nil
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{
				domain.FieldWorkEmail: {Value: s.Email, Confidence: 0.9},
			}}
			return res, true, nil
		},
	}

	res, err := a.Fetch(context.Background(), provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := res.Values[domain.FieldWorkEmail].Value; got != "jane@acme.com" {
		t.Fatalf("value = %q, want jane@acme.com", got)
	}
	if pollCount < 2 {
		t.Fatalf("expected the loop to poll until done (>=2), polled %d", pollCount)
	}
}

// TestAsyncHTTPAdapter_PollBudgetExpires proves the poll loop is bounded: a job that never finishes
// is abandoned when the ctx deadline passes, classified TRANSIENT (never sleeps past ctx.Done()).
func TestAsyncHTTPAdapter_PollBudgetExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/submit") {
			_, _ = w.Write([]byte(`{"job_id":"j1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"pending"}`)) // never terminal
	}))
	defer srv.Close()

	a := &provider.AsyncHTTPAdapter{
		NameV:        "slow",
		BaseURL:      srv.URL,
		Client:       srv.Client(),
		PollInterval: 5 * time.Millisecond,
		Submit: func(ctx context.Context, base string, _ provider.Request) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, base+"/submit", nil)
		},
		ParseSubmit: func(body []byte) (string, error) { return "j1", nil },
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/jobs/"+token, nil)
		},
		Decode: func(body []byte) (provider.Result, bool, error) {
			return provider.Result{}, false, nil // always pending
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := a.Fetch(ctx, provider.Request{})
	if err == nil {
		t.Fatal("expected a timeout error from an unbounded poll, got nil")
	}
	if domain.ClassOf(err) != domain.ClassTransient {
		t.Fatalf("poll-budget-expiry class = %s, want TRANSIENT", domain.ClassOf(err))
	}
}

// TestAsyncHTTPAdapter_PolicyOverride confirms it advertises a longer bounded budget by default.
func TestAsyncHTTPAdapter_PolicyOverride(t *testing.T) {
	var a provider.Adapter = &provider.AsyncHTTPAdapter{NameV: "x"}
	po, ok := a.(provider.PolicyOverrider)
	if !ok {
		t.Fatal("AsyncHTTPAdapter should implement PolicyOverrider")
	}
	if got := po.CallPolicy(); got.Timeout < 10*time.Second {
		t.Fatalf("default async budget too short: %s", got.Timeout)
	}
}
