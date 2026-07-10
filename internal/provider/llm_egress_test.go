package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/domain"
)

// TestWithAuthDescriptor_InjectsBearer proves the exported egress seam: a non-HTTPAdapter caller
// (e.g. internal/ai) that attaches an AuthDescriptor via WithAuthDescriptor gets the leased key
// injected by the AuthInjector transport exactly as HTTPAdapter would — the secret never leaves the
// egress boundary.
func TestWithAuthDescriptor_InjectsBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewAuthInjector(http.DefaultTransport, StaticKeyResolver{"pool:default": "secret42"})}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithAuthDescriptor(context.Background(), AuthDescriptor{Scheme: AuthBearer, KeyPoolSelector: "pool:default"})
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer secret42" {
		t.Fatalf("auth header = %q, want Bearer secret42", gotAuth)
	}
}

// TestWithAuthDescriptor_NoSelectorNoInjection proves a descriptor with no key-pool selector is a
// pass-through (public search/dataset APIs, AuthNone).
func TestWithAuthDescriptor_NoSelectorNoInjection(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	client := &http.Client{Transport: NewAuthInjector(http.DefaultTransport, StaticKeyResolver{})}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	ctx := WithAuthDescriptor(context.Background(), AuthDescriptor{Scheme: AuthBearer})
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "" {
		t.Fatalf("auth header = %q, want empty (no selector ⇒ no injection)", gotAuth)
	}
}

func TestClassifyStatus_Exported(t *testing.T) {
	cases := []struct {
		code  int
		class domain.ErrorClass
		ok    bool
	}{
		{200, domain.ClassUnknown, true},
		{401, domain.ClassAuth, false},
		{402, domain.ClassQuota, false},
		{429, domain.ClassRateLimit, false},
		{404, domain.ClassNotFound, false},
		{500, domain.ClassTransient, false},
		{503, domain.ClassProviderDown, false},
	}
	for _, tc := range cases {
		class, ok := ClassifyStatus(tc.code)
		if ok != tc.ok || class != tc.class {
			t.Fatalf("ClassifyStatus(%d) = (%v,%v), want (%v,%v)", tc.code, class, ok, tc.class, tc.ok)
		}
	}
}
