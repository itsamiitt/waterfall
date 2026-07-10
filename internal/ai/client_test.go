package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// egressWith builds an egress client whose AuthInjector transport leases keys from resolver — the
// same seam the enrichment adapters use, proving the LLM client reuses it (ADR-0026).
func egressWith(resolver provider.StaticKeyResolver) *http.Client {
	return &http.Client{Transport: provider.NewAuthInjector(http.DefaultTransport, resolver)}
}

func TestComplete_OpenAIDialect_InjectsKeyAndParsesUsage(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"summary\":\"ok\"}"}}],"usage":{"prompt_tokens":11,"completion_tokens":22}}`)
	}))
	defer srv.Close()

	c := NewLLMClient(egressWith(provider.StaticKeyResolver{"openrouter:default": "sk-test-123"}))
	m := Model{Slug: "openrouter", ModelID: "m1", BaseURL: srv.URL, Dialect: DialectOpenAI, Auth: bearer("openrouter:default")}

	comp, err := c.Complete(context.Background(), m, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hi"}}, JSON: true, MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "Bearer sk-test-123" {
		t.Fatalf("auth header = %q, want Bearer sk-test-123 (egress injection)", gotAuth)
	}
	if !strings.Contains(gotBody, `"response_format"`) || !strings.Contains(gotBody, `"m1"`) {
		t.Fatalf("request body missing model/response_format: %s", gotBody)
	}
	if !strings.Contains(comp.Text, `"summary":"ok"`) {
		t.Fatalf("completion text = %q", comp.Text)
	}
	if comp.Usage.InputTokens != 11 || comp.Usage.OutputTokens != 22 {
		t.Fatalf("usage = %+v, want 11/22", comp.Usage)
	}
	if comp.Model != "m1" {
		t.Fatalf("model = %q, want m1", comp.Model)
	}
}

func TestComplete_AnthropicDialect(t *testing.T) {
	var gotKey, gotVer, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"hello world"}],"usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	defer srv.Close()

	c := NewLLMClient(egressWith(provider.StaticKeyResolver{"anthropic:default": "ak-1"}))
	m := Model{Slug: "anthropic", ModelID: "claude", BaseURL: srv.URL, Dialect: DialectAnthropic, Auth: apiKeyHeader("anthropic:default", "x-api-key")}

	comp, err := c.Complete(context.Background(), m, CompletionRequest{
		Messages: []Message{{Role: "system", Content: "be terse"}, {Role: "user", Content: "hi"}}, MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotKey != "ak-1" || gotVer != "2023-06-01" {
		t.Fatalf("headers key=%q ver=%q", gotKey, gotVer)
	}
	if !strings.Contains(gotBody, `"system":"be terse"`) {
		t.Fatalf("anthropic body should carry system top-level: %s", gotBody)
	}
	if comp.Text != "hello world" || comp.Usage.InputTokens != 5 || comp.Usage.OutputTokens != 7 {
		t.Fatalf("got text=%q usage=%+v", comp.Text, comp.Usage)
	}
}

func TestComplete_StatusClassification(t *testing.T) {
	cases := []struct {
		code int
		want domain.ErrorClass
	}{
		{http.StatusUnauthorized, domain.ClassAuth},
		{http.StatusPaymentRequired, domain.ClassQuota},
		{http.StatusTooManyRequests, domain.ClassRateLimit},
		{http.StatusNotFound, domain.ClassNotFound},
		{http.StatusInternalServerError, domain.ClassTransient},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			io.WriteString(w, `{"error":"x"}`)
		}))
		c := NewLLMClient(egressWith(provider.StaticKeyResolver{"openrouter:default": "k"}))
		m := Model{Slug: "openrouter", ModelID: "m", BaseURL: srv.URL, Dialect: DialectOpenAI, Auth: bearer("openrouter:default")}
		_, err := c.Complete(context.Background(), m, CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
		if err == nil {
			srv.Close()
			t.Fatalf("status %d: expected error", tc.code)
		}
		if got := domain.ClassOf(err); got != tc.want {
			srv.Close()
			t.Fatalf("status %d classified as %v, want %v", tc.code, got, tc.want)
		}
		srv.Close()
	}
}
