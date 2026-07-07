package provider_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// decode maps a tiny JSON provider response {"email":"..","score":0.9} into a Result.
func decode(body []byte) (provider.Result, error) {
	var payload struct {
		Email string  `json:"email"`
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return provider.Result{}, err
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if payload.Email != "" {
		res.Values[domain.FieldWorkEmail] = provider.Observation{
			Value:      payload.Email,
			Confidence: domain.Confidence(payload.Score),
		}
	}
	return res, nil
}

func TestHTTPAdapter_Success_KeyInjectedAtEgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The egress AuthInjector must have placed the real secret; the adapter never did.
		if got := r.Header.Get("X-API-Key"); got != "SECRET-123" {
			t.Errorf("expected injected key SECRET-123, got %q", got)
		}
		_, _ = w.Write([]byte(`{"email":"jane@acme.com","score":0.92}`))
	}))
	defer srv.Close()

	// Wrap the client transport with the egress injection seam.
	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"acme:default": "SECRET-123"})

	a := &provider.HTTPAdapter{
		NameV:   "acme-http",
		BaseURL: srv.URL,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyHeader, HeaderName: "X-API-Key", KeyPoolSelector: "acme:default"},
		Caps:    []provider.Capability{{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.9}},
		Decode:  decode,
	}
	res, err := a.Fetch(context.Background(), provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	got := res.Values[domain.FieldWorkEmail]
	if got.Value != "jane@acme.com" || got.Confidence != 0.92 {
		t.Fatalf("unexpected result: %+v", got)
	}
}

// TestAuthInjector_OAuth2CC proves ADR-0024 Phase 2: for an oauth2-cc adapter the egress
// AuthInjector exchanges the pool secret for a bearer token at TokenURL, injects it as
// Authorization: Bearer <token> on the data call, and CACHES it — a second call reuses the
// token without re-hitting the token endpoint. The adapter never holds the secret.
func TestAuthInjector_OAuth2CC(t *testing.T) {
	var tokenHits int
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenHits++
		if got := r.Header.Get("Authorization"); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("cid:csecret")) {
			t.Errorf("token exchange missing Basic client creds, got %q", got)
		}
		_, _ = w.Write([]byte(`{"access_token":"ACCESS-XYZ","expiresIn":86400}`))
	}))
	defer tokenSrv.Close()

	dataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ACCESS-XYZ" {
			t.Errorf("data call missing injected bearer, got %q", got)
		}
		_, _ = w.Write([]byte(`{"email":"jane@acme.com","score":0.9}`))
	}))
	defer dataSrv.Close()

	client := dataSrv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"dnb:default": "cid:csecret"})

	a := &provider.HTTPAdapter{
		NameV:   "dnb",
		BaseURL: dataSrv.URL,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthOAuth2CC,
			KeyPoolSelector: "dnb:default",
			TokenURL:        tokenSrv.URL,
		},
		Caps:   []provider.Capability{{Field: domain.FieldWorkEmail, Cost: 5, ExpectedConfidence: 0.9}},
		Decode: decode,
	}
	for i := 0; i < 2; i++ {
		res, err := a.Fetch(context.Background(), provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}})
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		if res.Values[domain.FieldWorkEmail].Value != "jane@acme.com" {
			t.Fatalf("fetch %d: unexpected result %+v", i, res.Values)
		}
	}
	if tokenHits != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (cached across calls)", tokenHits)
	}
}

// TestAuthInjector_APIKeyPath proves ADR-0024 Phase 4: for an api-key-path adapter the egress
// AuthInjector substitutes the adapter's path sentinel with the leased key, so the key travels as a
// URL path segment (MixRank's shape) — the adapter itself never writes the secret.
func TestAuthInjector_APIKeyPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"email":"jane@acme.com","score":0.9}`))
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = provider.NewAuthInjector(client.Transport, provider.StaticKeyResolver{"pathco:default": "KEY123"})

	a := &provider.HTTPAdapter{
		NameV:   "pathco",
		BaseURL: srv.URL,
		Client:  client,
		Auth:    provider.AuthDescriptor{Scheme: provider.AuthAPIKeyPath, PathPlaceholder: "PLACEHOLDERKEY", KeyPoolSelector: "pathco:default"},
		Caps:    []provider.Capability{{Field: domain.FieldWorkEmail, Cost: 2, ExpectedConfidence: 0.9}},
		Build: func(ctx context.Context, base string, _ provider.Request) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/PLACEHOLDERKEY/lookup", nil)
		},
		Decode: decode,
	}
	if _, err := a.Fetch(context.Background(), provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotPath != "/v2/KEY123/lookup" {
		t.Fatalf("path key not injected: server saw %q, want /v2/KEY123/lookup", gotPath)
	}
}

func TestHTTPAdapter_StatusTaxonomy(t *testing.T) {
	cases := []struct {
		status int
		want   domain.ErrorClass
	}{
		{http.StatusUnauthorized, domain.ClassAuth},
		{http.StatusPaymentRequired, domain.ClassQuota},
		{http.StatusForbidden, domain.ClassRateLimit},
		{http.StatusTooManyRequests, domain.ClassRateLimit},
		{http.StatusBadRequest, domain.ClassBadRequest},
		{http.StatusServiceUnavailable, domain.ClassProviderDown},
		{http.StatusInternalServerError, domain.ClassTransient},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
		}))
		a := &provider.HTTPAdapter{NameV: "x", BaseURL: srv.URL, Client: srv.Client(), Decode: decode}
		_, err := a.Fetch(context.Background(), provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}})
		if domain.ClassOf(err) != tc.want {
			t.Errorf("status %d: want class %s, got %s", tc.status, tc.want, domain.ClassOf(err))
		}
		srv.Close()
	}
}

func TestHTTPAdapter_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"email":"x@y.com","score":0.5}`))
	}))
	defer srv.Close()

	a := &provider.HTTPAdapter{NameV: "slow", BaseURL: srv.URL, Client: srv.Client(), Decode: decode}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := a.Fetch(ctx, provider.Request{Fields: []domain.Field{domain.FieldWorkEmail}})
	if domain.ClassOf(err) != domain.ClassTransient {
		t.Fatalf("timeout should be transient, got %v", err)
	}
}
