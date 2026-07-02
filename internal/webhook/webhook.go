// Package webhook delivers job-completion callbacks to tenants. Delivery is:
//   - tenant-bound: the target URL comes ONLY from the delivering job's tenant's
//     registered config (resolved by tenant_id), never from request data, so tenant A's
//     enriched PII can never be sent to tenant B's endpoint (G1, docs/13 §6, docs/18 §2);
//   - SSRF-safe: sent through an egress client whose allow-list is that one tenant host
//     (the Slice-05 choke);
//   - authenticated: the body is HMAC-SHA256 signed with the tenant's secret so the
//     receiver can verify origin + integrity.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/job"
)

// Config is a tenant's webhook endpoint.
type Config struct {
	URL    string
	Secret string
}

// Registry resolves a tenant's webhook config. Get returns ok=false when the tenant has no
// webhook configured (delivery is then skipped, not an error).
type Registry interface {
	Get(tenantID string) (Config, bool)
}

// MemoryRegistry is a static tenant->Config map.
type MemoryRegistry map[string]Config

// Get implements Registry.
func (m MemoryRegistry) Get(tenantID string) (Config, bool) {
	c, ok := m[tenantID]
	return c, ok
}

// Sign returns the "sha256=<hex>" signature of body under secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is a valid signature of body under secret (constant-time).
func Verify(secret string, body []byte, sig string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(sig))
}

// Sender delivers completion callbacks. clientFor returns the HTTP client to use for a
// given target host — in production it is provider.NewEgressClient with a per-tenant
// allow-list (tenant-bound + SSRF-safe); tests inject a plain client.
type Sender struct {
	reg         Registry
	clientFor   func(host string) *http.Client
	maxAttempts int
	backoff     time.Duration
	sleep       func(context.Context, time.Duration)
}

// Option configures a Sender.
type Option func(*Sender)

// WithMaxAttempts sets the bounded delivery attempt count (default 3).
func WithMaxAttempts(n int) Option { return func(s *Sender) { s.maxAttempts = n } }

// WithBackoff sets the base backoff between attempts (default 200ms; 0 for tests).
func WithBackoff(d time.Duration) Option { return func(s *Sender) { s.backoff = d } }

// NewSender builds a webhook Sender.
func NewSender(reg Registry, clientFor func(host string) *http.Client, opts ...Option) *Sender {
	s := &Sender{
		reg:         reg,
		clientFor:   clientFor,
		maxAttempts: 3,
		backoff:     200 * time.Millisecond,
		sleep: func(ctx context.Context, d time.Duration) {
			if d <= 0 {
				return
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Deliver sends the completion callback for j. It is a no-op (nil) when the tenant has no
// webhook configured. Delivery is best-effort with bounded retries; a persistent failure
// returns an error for the caller to log, but never affects the (already durable) job.
func (s *Sender) Deliver(ctx context.Context, j *job.Job) error {
	cfg, ok := s.reg.Get(j.TenantID)
	if !ok {
		return nil // no webhook configured for this tenant
	}
	body, err := buildPayload(j)
	if err != nil {
		return err
	}
	host, err := hostOf(cfg.URL)
	if err != nil {
		return err
	}
	sig := Sign(cfg.Secret, body)
	client := s.clientFor(host)
	event := eventName(j.Status)

	var lastErr error
	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Waterfall-Event", event)
		req.Header.Set("X-Waterfall-Signature", sig)

		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil // delivered
			}
			lastErr = fmt.Errorf("webhook returned %d", resp.StatusCode)
			// 4xx other than 429 is terminal — the endpoint rejected us; don't retry.
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return lastErr
			}
		} else {
			lastErr = err
		}
		if attempt < s.maxAttempts {
			s.sleep(ctx, s.backoff<<(attempt-1))
		}
	}
	return lastErr
}

// --- payload ---

type fieldPayload struct {
	Value          string  `json:"value"`
	Confidence     float64 `json:"confidence"`
	Provider       string  `json:"provider"`
	CostCredits    int64   `json:"cost_credits"`
	IdempotencyKey string  `json:"idempotency_key"`
	ObservedAt     string  `json:"observed_at"`
}

type payload struct {
	Event     string                  `json:"event"`
	JobID     string                  `json:"job_id"`
	Status    string                  `json:"status"`
	Committed int64                   `json:"committed_credits"`
	Filled    map[string]fieldPayload `json:"filled,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

func buildPayload(j *job.Job) ([]byte, error) {
	p := payload{Event: eventName(j.Status), JobID: j.ID, Status: string(j.Status), Error: j.Err}
	if j.Outcome != nil {
		p.Committed = int64(j.Outcome.Committed)
		p.Filled = map[string]fieldPayload{}
		for f, v := range j.Outcome.Filled {
			p.Filled[string(f)] = fieldPayload{
				Value:          v.Value,
				Confidence:     float64(v.Confidence),
				Provider:       v.Prov.Provider,
				CostCredits:    int64(v.Prov.CostCredits),
				IdempotencyKey: v.Prov.IdempotencyKey,
				ObservedAt:     v.Prov.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
	}
	return json.Marshal(p)
}

func eventName(status job.Status) string {
	if status == job.StatusFailed {
		return "enrichment.failed"
	}
	return "enrichment.completed"
}

func hostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("webhook url %q has no host", rawURL)
	}
	return u.Hostname(), nil
}
