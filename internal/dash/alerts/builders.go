package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/enrichment/waterfall/internal/provider"
)

// notifyPayload is the alert content a channel renders. DedupeKey rides every delivery as a
// correlation id (PagerDuty/Opsgenie-compatible), never as the uniqueness mechanism (doc 10 §5.2).
type notifyPayload struct {
	Occasion  string            `json:"occasion"`
	RuleName  string            `json:"rule_name"`
	Metric    string            `json:"metric"`
	Severity  string            `json:"severity,omitempty"`
	State     string            `json:"state"`
	Value     float64           `json:"value"`
	Scope     map[string]string `json:"scope,omitempty"`
	DedupeKey string            `json:"dedupe_key"`
	FiredAt   time.Time         `json:"fired_at"`
}

// deliveryResult is the uniform outcome across HTTP and SMTP channels.
type deliveryResult struct {
	ok         bool
	statusCode int
	blocked    bool // SSRF-refused (result="ssrf_blocked", doc 10 §5.4)
	note       string
}

// HTTPDoer is the consumer-side slice of *http.Client the builders need. Satisfied by *http.Client
// (production: the SSRF-guarded provider.NewEgressClient; tests may inject a permissive client to
// exercise a loopback sink). Exported so the orchestrator and tests can supply an EgressFactory.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// egressFactory builds the outbound client for a webhook host. The default (defaultEgress) returns
// the SSRF-guarded provider egress client scoped to exactly that host, so a private-range / metadata
// dial is refused at connect time even though the host itself is on the (derived) allow-list.
type egressFactory func(host string) HTTPDoer

func defaultEgress(host string) HTTPDoer {
	return provider.NewEgressClient(provider.NewHostAllowList(host), nil)
}

// deliver dispatches to the per-kind builder. email rides the guarded SMTP dialer; every other kind
// is an HTTP POST over the SSRF-guarded egress client + HMAC signing.
func deliver(ctx context.Context, factory egressFactory, kind string, cfg ChannelConfig, p notifyPayload) deliveryResult {
	if kind == "email" {
		return deliverEmail(ctx, cfg, p)
	}
	return deliverHTTP(ctx, factory, kind, cfg, p)
}

// deliverHTTP POSTs the rendered body through the SSRF-guarded egress client with HMAC signing. An
// SSRF refusal is reported as blocked (mapped to egress_blocked / result="ssrf_blocked"); a non-2xx
// response is a delivery failure that the notifier backs off.
func deliverHTTP(ctx context.Context, factory egressFactory, kind string, cfg ChannelConfig, p notifyPayload) deliveryResult {
	if factory == nil {
		factory = defaultEgress
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" {
		return deliveryResult{note: "channel url is empty or invalid"}
	}
	body := renderBody(kind, p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return deliveryResult{note: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Waterfall-Dedupe", p.DedupeKey)
	if cfg.Secret != "" {
		req.Header.Set("X-Waterfall-Signature", "sha256="+hmacHex(cfg.Secret, body))
	}
	resp, err := factory(u.Hostname()).Do(req)
	if err != nil {
		if errors.Is(err, provider.ErrSSRFBlocked) {
			return deliveryResult{blocked: true, note: "egress blocked by SSRF policy"}
		}
		return deliveryResult{note: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // bounded response drain
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return deliveryResult{ok: ok, statusCode: resp.StatusCode, note: resp.Status}
}

// renderBody shapes the JSON body per channel kind. slack/teams use {"text":...}; discord uses
// {"content":...}; webhook carries the full structured payload.
func renderBody(kind string, p notifyPayload) []byte {
	summary := p.State + ": " + p.RuleName + " (" + p.Metric + ")"
	switch kind {
	case "slack", "teams":
		b, _ := json.Marshal(map[string]any{"text": summary, "alert": p})
		return b
	case "discord":
		b, _ := json.Marshal(map[string]any{"content": summary, "alert": p})
		return b
	default: // webhook
		b, _ := json.Marshal(p)
		return b
	}
}

// hmacHex is HMAC-SHA256(secret, body) hex-encoded (webhook signature).
func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
