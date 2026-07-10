package ai

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

// Message is one chat message.
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// CompletionRequest is a single LLM call's inputs.
type CompletionRequest struct {
	Messages    []Message
	MaxTokens   int
	Temperature float64 // pin low (≈0) for cache-leaning determinism (ADR-0026 nondeterminism contract)
	JSON        bool    // request a JSON-object response where the dialect supports it
}

// Usage is the token accounting from a completion (G4 charge-on-actual, G5 provenance).
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Completion is a successful LLM response.
type Completion struct {
	Text  string
	Usage Usage
	Model string // the ModelID that produced it (provenance)
}

// Completer is the seam RunCascade depends on. *LLMClient implements it; tests substitute a fake so
// the cascade's deterministic dispose logic is exercised without any HTTP.
type Completer interface {
	Complete(ctx context.Context, m Model, req CompletionRequest) (Completion, error)
}

// LLMClient performs one bounded LLM call through the egress *http.Client (whose AuthInjector
// transport leases and injects the key). It holds NO secret. A per-model circuit breaker (G3) trips
// on provider ill-health. Safe for concurrent use.
type LLMClient struct {
	HTTP   *http.Client        // the egress client (AuthInjector transport). Required in production.
	Policy provider.CallPolicy // bounded budget per call; {90s, 1} when zero.
	now    func() time.Time

	mu       sync.Mutex
	breakers map[string]*provider.Breaker
}

var errBreakerOpen = errors.New("llm circuit breaker open")

// NewLLMClient builds a client over the egress HTTP client.
func NewLLMClient(egress *http.Client) *LLMClient {
	return &LLMClient{HTTP: egress, now: time.Now, breakers: map[string]*provider.Breaker{}}
}

func (c *LLMClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *LLMClient) breakerFor(slug string) *provider.Breaker {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.breakers == nil {
		c.breakers = map[string]*provider.Breaker{}
	}
	if br, ok := c.breakers[slug]; ok {
		return br
	}
	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}
	br := provider.NewBreaker(5, 30*time.Second, nowFn)
	c.breakers[slug] = br
	return br
}

// Complete makes one bounded, breaker-guarded call to m. G3: hard timeout + breaker + no internal
// retry (MaxAttempts is 1 — the cascade, not this call, escalates). The secret stays at the egress
// boundary (provider.WithAuthDescriptor). Failures are the classified provider taxonomy.
func (c *LLMClient) Complete(ctx context.Context, m Model, req CompletionRequest) (Completion, error) {
	pol := c.Policy
	if pol.Timeout <= 0 {
		pol = provider.CallPolicy{Timeout: 90 * time.Second, MaxAttempts: 1}
	}
	br := c.breakerFor(m.Slug)
	if !br.Allow() {
		return Completion{}, domain.NewProviderError(m.Slug, domain.ClassProviderDown, errBreakerOpen)
	}

	httpReq, err := buildRequest(ctx, m, req)
	if err != nil {
		return Completion{}, domain.NewProviderError(m.Slug, domain.ClassBadRequest, err)
	}
	// Attach the egress auth descriptor so the AuthInjector leases + injects the key at the
	// boundary — the AI layer never holds a secret.
	if m.Auth.KeyPoolSelector != "" {
		httpReq = httpReq.WithContext(provider.WithAuthDescriptor(httpReq.Context(), m.Auth))
	}
	cctx, cancel := context.WithTimeout(httpReq.Context(), pol.Timeout)
	defer cancel()
	httpReq = httpReq.WithContext(cctx)

	resp, err := c.httpClient().Do(httpReq)
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
		return Completion{}, domain.NewProviderError(m.Slug, class, err)
	}
	defer resp.Body.Close()

	if class, ok := provider.ClassifyStatus(resp.StatusCode); !ok {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if class == domain.ClassRateLimit || class == domain.ClassTransient || class == domain.ClassProviderDown {
			br.RecordFailure()
		}
		return Completion{}, domain.NewProviderError(m.Slug, class,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(b)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		br.RecordFailure()
		return Completion{}, domain.NewProviderError(m.Slug, domain.ClassTransient, err)
	}
	comp, err := parseResponse(m, body)
	if err != nil {
		return Completion{}, domain.NewProviderError(m.Slug, domain.ClassBadRequest, err)
	}
	br.RecordSuccess()
	comp.Model = m.ModelID
	return comp, nil
}

// buildRequest constructs the wire request for the model's dialect. It sets only NON-secret headers
// (content-type, anthropic-version); the credential is injected at egress.
func buildRequest(ctx context.Context, m Model, req CompletionRequest) (*http.Request, error) {
	if m.Dialect == DialectAnthropic {
		return buildAnthropic(ctx, m, req)
	}
	return buildOpenAI(ctx, m, req)
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func toWireMessages(ms []Message) []wireMessage {
	out := make([]wireMessage, 0, len(ms))
	for _, m := range ms {
		out = append(out, wireMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

func buildOpenAI(ctx context.Context, m Model, req CompletionRequest) (*http.Request, error) {
	body := map[string]any{
		"model":       m.ModelID,
		"messages":    toWireMessages(req.Messages),
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.JSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	return httpReq, nil
}

func buildAnthropic(ctx context.Context, m Model, req CompletionRequest) (*http.Request, error) {
	// Anthropic carries system prompts in a top-level "system" field, not the messages array.
	var system string
	msgs := make([]wireMessage, 0, len(req.Messages))
	for _, mm := range req.Messages {
		if mm.Role == "system" {
			if system != "" {
				system += "\n\n"
			}
			system += mm.Content
			continue
		}
		msgs = append(msgs, wireMessage{Role: mm.Role, Content: mm.Content})
	}
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = 1024 // Anthropic requires max_tokens
	}
	body := map[string]any{
		"model":       m.ModelID,
		"max_tokens":  maxTok,
		"temperature": req.Temperature,
		"messages":    msgs,
	}
	if system != "" {
		body["system"] = system
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	return httpReq, nil
}

// parseResponse decodes a 2xx body for the model's dialect into text + token usage.
func parseResponse(m Model, body []byte) (Completion, error) {
	if m.Dialect == DialectAnthropic {
		var r struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return Completion{}, err
		}
		var text string
		for _, b := range r.Content {
			if b.Type == "text" || b.Type == "" {
				text += b.Text
			}
		}
		return Completion{Text: text, Usage: Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}}, nil
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Completion{}, err
	}
	if len(r.Choices) == 0 {
		return Completion{}, errors.New("no choices in completion")
	}
	return Completion{
		Text:  r.Choices[0].Message.Content,
		Usage: Usage{InputTokens: r.Usage.PromptTokens, OutputTokens: r.Usage.CompletionTokens},
	}, nil
}
