package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Pusher performs one bounded CRM write through the egress *http.Client — THE single internet boundary
// (ADR-0030/0010). It holds NO secret: the OAuth token is injected at egress via an AuthDescriptor keyed on
// the connection's envelope reference, never in the control-plane. A per-provider circuit breaker (G3)
// trips on ill-health. A private/loopback or non-allowlisted host is refused by the egress SSRF guard.
// Safe for concurrent use.
type Pusher struct {
	HTTP   *http.Client        // the egress client (SSRF guard + AuthInjector). Required in production.
	Policy provider.CallPolicy // bounded budget per push; {20s, 1} when zero.
	now    func() time.Time

	mu       sync.Mutex
	breakers map[string]*provider.Breaker
}

var errBreakerOpen = errors.New("crm circuit breaker open")

// NewPusher builds a CRM pusher over the egress HTTP client.
func NewPusher(egress *http.Client) *Pusher {
	return &Pusher{HTTP: egress, now: time.Now, breakers: map[string]*provider.Breaker{}}
}

func (p *Pusher) httpClient() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func (p *Pusher) breakerFor(slug string) *provider.Breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.breakers == nil {
		p.breakers = map[string]*provider.Breaker{}
	}
	if br, ok := p.breakers[slug]; ok {
		return br
	}
	nowFn := p.now
	if nowFn == nil {
		nowFn = time.Now
	}
	br := provider.NewBreaker(5, 30*time.Second, nowFn)
	p.breakers[slug] = br
	return br
}

// PushInput is one CRM write.
type PushInput struct {
	Endpoint  string          // the CRM write URL (https; host must be on the egress allow-list)
	SecretRef string          // envelope reference selecting the OAuth token at egress (never the token)
	Body      json.RawMessage // the mapped record body
}

// Push writes one record to a CRM through the egress boundary. G3: hard timeout + breaker. The token is
// injected at egress (provider.WithAuthDescriptor keyed on SecretRef); a private/loopback or non-allowlisted
// host is refused by the SSRF guard (ErrSSRFBlocked → ClassBadRequest). Errors are the classified taxonomy.
func (p *Pusher) Push(ctx context.Context, providerSlug string, in PushInput) error {
	pol := p.Policy
	if pol.Timeout <= 0 {
		pol = provider.CallPolicy{Timeout: 20 * time.Second, MaxAttempts: 1}
	}
	br := p.breakerFor(providerSlug)
	if !br.Allow() {
		return domain.NewProviderError(providerSlug, domain.ClassProviderDown, errBreakerOpen)
	}

	body := in.Body
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, in.Endpoint, bytes.NewReader(body))
	if err != nil {
		return domain.NewProviderError(providerSlug, domain.ClassBadRequest, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if in.SecretRef != "" {
		req = req.WithContext(provider.WithAuthDescriptor(req.Context(),
			provider.AuthDescriptor{Scheme: provider.AuthBearer, KeyPoolSelector: in.SecretRef}))
	}
	cctx, cancel := context.WithTimeout(req.Context(), pol.Timeout)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := p.httpClient().Do(req)
	if err != nil {
		class := domain.ClassProviderDown
		switch {
		case errors.Is(err, provider.ErrSSRFBlocked):
			class = domain.ClassBadRequest
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			class = domain.ClassTransient
		}
		if class == domain.ClassTransient || class == domain.ClassProviderDown {
			br.RecordFailure()
		}
		return domain.NewProviderError(providerSlug, class, err)
	}
	defer resp.Body.Close()

	if class, ok := provider.ClassifyStatus(resp.StatusCode); !ok {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if class == domain.ClassRateLimit || class == domain.ClassTransient || class == domain.ClassProviderDown {
			br.RecordFailure()
		}
		return domain.NewProviderError(providerSlug, class, fmt.Errorf("status %d: %s", resp.StatusCode, string(b)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	br.RecordSuccess()
	return nil
}

// ApplyFieldMap projects source dossier fields through a {dossier_field: crm_field} mapping into a CRM
// record body {crm_field: value}. Only mapped fields present in src are written — an unmapped or absent
// field never leaks. Deterministic (json.Marshal sorts keys).
func ApplyFieldMap(mapping map[string]string, src map[string]string) json.RawMessage {
	out := map[string]string{}
	for dossierField, crmField := range mapping {
		if v, ok := src[dossierField]; ok {
			out[crmField] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}
