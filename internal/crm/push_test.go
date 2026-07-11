package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

func egressWith(resolver provider.StaticKeyResolver) *http.Client {
	return &http.Client{Transport: provider.NewAuthInjector(http.DefaultTransport, resolver)}
}

func TestPush_InjectsTokenAtEgressAndPosts(t *testing.T) {
	var gotAuth, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPusher(egressWith(provider.StaticKeyResolver{"env-1": "tok-xyz"}))
	err := p.Push(context.Background(), "hubspot", PushInput{
		Endpoint: srv.URL, SecretRef: "env-1", Body: json.RawMessage(`{"Ticker__c":"ACME"}`),
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotAuth != "Bearer tok-xyz" {
		t.Fatalf("Authorization = %q, want Bearer tok-xyz (egress injection, not control-plane)", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if !strings.Contains(gotBody, `"Ticker__c":"ACME"`) {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestPush_StatusClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := NewPusher(egressWith(provider.StaticKeyResolver{"env-1": "t"}))
	err := p.Push(context.Background(), "hubspot", PushInput{Endpoint: srv.URL, SecretRef: "env-1"})
	if err == nil {
		t.Fatal("401 must be an error")
	}
	if got := domain.ClassOf(err); got != domain.ClassAuth {
		t.Fatalf("401 classified %v, want ClassAuth", got)
	}
}

func TestPush_SSRFBlocked_RFC1918(t *testing.T) {
	// The real egress client: SSRF dial guard + host allow-list. Even with the private host on the
	// allow-list, the dial guard refuses the RFC1918 address — every CRM push traverses the single
	// boundary and a private host is refused (ADR-0030 acceptance #3).
	egress := provider.NewEgressClient(provider.NewHostAllowList("10.0.0.1"), provider.StaticKeyResolver{"env-1": "t"})
	p := NewPusher(egress)
	err := p.Push(context.Background(), "hubspot", PushInput{Endpoint: "https://10.0.0.1/crm", SecretRef: "env-1"})
	if err == nil {
		t.Fatal("push to an RFC1918 host must be refused")
	}
	if !errors.Is(err, provider.ErrSSRFBlocked) && domain.ClassOf(err) != domain.ClassBadRequest {
		t.Fatalf("SSRF refusal = %v, want ErrSSRFBlocked / ClassBadRequest", err)
	}
}

func TestApplyFieldMap(t *testing.T) {
	got := ApplyFieldMap(
		map[string]string{"company_ticker": "Ticker__c", "total_funding_usd": "Funding__c", "unmapped": "X__c"},
		map[string]string{"company_ticker": "ACME", "total_funding_usd": "1000000"},
	)
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["Ticker__c"] != "ACME" || m["Funding__c"] != "1000000" {
		t.Fatalf("mapping wrong: %v", m)
	}
	if _, ok := m["X__c"]; ok {
		t.Fatal("a source field absent from src must not be written")
	}
}
