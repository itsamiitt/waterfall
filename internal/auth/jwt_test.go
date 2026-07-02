package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/auth"
	"github.com/enrichment/waterfall/internal/auth/authtest"
)

var (
	fixedNow = time.Unix(1_700_000_000, 0)
	secret   = []byte("super-secret-signing-key-0123456789")
)

func baseClaims() map[string]any {
	return map[string]any{
		"sub":       "user-1",
		"iss":       "https://issuer.example",
		"aud":       "enrichment-api",
		"exp":       fixedNow.Add(time.Hour).Unix(),
		"iat":       fixedNow.Unix(),
		"tenant_id": "tenant-A",
		"scope":     "enrich:write enrich:read",
	}
}

func hsVerifier() *auth.Verifier {
	v := auth.NewVerifier(
		auth.WithIssuer("https://issuer.example"),
		auth.WithAudience("enrichment-api"),
		auth.WithClock(func() time.Time { return fixedNow }),
	)
	v.AddHMACKey("k1", secret)
	return v
}

func TestVerify_ValidHS256(t *testing.T) {
	tok := authtest.SignHS256(secret, "k1", baseClaims())
	c, err := hsVerifier().Verify(tok)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if c.TenantID != "tenant-A" || c.Subject != "user-1" {
		t.Fatalf("bad claims: %+v", c)
	}
	if len(c.Scopes) != 2 || c.Scopes[0] != "enrich:write" {
		t.Fatalf("scopes not parsed: %v", c.Scopes)
	}
}

func TestVerify_ValidRS256_AndRotation(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v := auth.NewVerifier(auth.WithClock(func() time.Time { return fixedNow }))
	v.AddHMACKey("k1", secret)         // old key still trusted
	v.AddRSAKey("k2", &priv.PublicKey) // rotated-in key
	tok := authtest.SignRS256(priv, "k2", baseClaims())
	c, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("valid RS256 rejected: %v", err)
	}
	if c.TenantID != "tenant-A" {
		t.Fatalf("bad tenant: %s", c.TenantID)
	}
}

func TestVerify_Rejects(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)

	cases := []struct {
		name string
		tok  func() string
		want error
	}{
		{"expired", func() string {
			c := baseClaims()
			c["exp"] = fixedNow.Add(-time.Hour).Unix()
			return authtest.SignHS256(secret, "k1", c)
		}, auth.ErrExpired},
		{"not-yet-valid", func() string {
			c := baseClaims()
			c["nbf"] = fixedNow.Add(time.Hour).Unix()
			return authtest.SignHS256(secret, "k1", c)
		}, auth.ErrNotYetValid},
		{"wrong-issuer", func() string {
			c := baseClaims()
			c["iss"] = "https://evil.example"
			return authtest.SignHS256(secret, "k1", c)
		}, auth.ErrIssuer},
		{"wrong-audience", func() string {
			c := baseClaims()
			c["aud"] = "some-other-api"
			return authtest.SignHS256(secret, "k1", c)
		}, auth.ErrAudience},
		{"missing-tenant", func() string {
			c := baseClaims()
			delete(c, "tenant_id")
			return authtest.SignHS256(secret, "k1", c)
		}, auth.ErrNoTenant},
		{"unknown-kid", func() string {
			return authtest.SignHS256(secret, "nope", baseClaims())
		}, auth.ErrUnknownKID},
		{"tampered-payload", func() string {
			// Keep well-formed segments but swap in a DIFFERENT payload under the original
			// signature: content no longer matches the MAC -> ErrBadSignature (not Malformed).
			good := authtest.SignHS256(secret, "k1", baseClaims())
			other := baseClaims()
			other["tenant_id"] = "tenant-EVIL"
			evil := authtest.SignHS256(secret, "k1", other)
			gp := strings.Split(good, ".")
			ep := strings.Split(evil, ".")
			return ep[0] + "." + ep[1] + "." + gp[2] // evil header+payload, original signature
		}, auth.ErrBadSignature},
		{"alg-none", func() string {
			return authtest.NoneToken(baseClaims())
		}, auth.ErrUnsupportedAlg},
		{"malformed", func() string { return "not-a-jwt" }, auth.ErrMalformed},
		{"wrong-secret", func() string {
			return authtest.SignHS256([]byte("attacker-key"), "k1", baseClaims())
		}, auth.ErrBadSignature},
	}

	v := hsVerifier()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(tc.tok())
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}

	// Algorithm-confusion: verifier holds an RSA key under kid "rk"; attacker forges an
	// HS256 token using the RSA PUBLIC key bytes as the HMAC secret. Must be rejected
	// because the alg is pinned by the key, not the header.
	t.Run("alg-confusion-rs-to-hs", func(t *testing.T) {
		vc := auth.NewVerifier(auth.WithClock(func() time.Time { return fixedNow }))
		vc.AddRSAKey("rk", &priv.PublicKey)
		pubBytes := x509.MarshalPKCS1PublicKey(&priv.PublicKey)
		forged := authtest.HS256WithKid(pubBytes, "rk", baseClaims())
		if _, err := vc.Verify(forged); !errors.Is(err, auth.ErrUnsupportedAlg) {
			t.Fatalf("alg-confusion must be rejected, got %v", err)
		}
	})
}

func TestVerify_AudienceArray(t *testing.T) {
	c := baseClaims()
	c["aud"] = []string{"other-api", "enrichment-api"}
	tok := authtest.SignHS256(secret, "k1", c)
	if _, err := hsVerifier().Verify(tok); err != nil {
		t.Fatalf("array audience containing the expected value should pass: %v", err)
	}
}

func TestVerify_LeewayAbsorbsSkew(t *testing.T) {
	c := baseClaims()
	c["exp"] = fixedNow.Add(-30 * time.Second).Unix() // expired 30s ago
	tok := authtest.SignHS256(secret, "k1", c)
	// default leeway is 60s, so a 30s-stale token still verifies
	if _, err := hsVerifier().Verify(tok); err != nil {
		t.Fatalf("30s skew within 60s leeway should pass: %v", err)
	}
}
