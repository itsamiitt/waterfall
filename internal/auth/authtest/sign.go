// Package authtest provides JWT signing helpers for tests only (the production auth package
// verifies but never signs). It mirrors the stdlib httptest pattern: test support that lives
// in its own package so multiple test suites can share it.
package authtest

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

func seg(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SignHS256 signs claims with an HS256 header (alg fixed to HS256, kid optional).
func SignHS256(secret []byte, kid string, claims map[string]any) string {
	h := map[string]any{"alg": "HS256", "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	input := seg(h) + "." + seg(claims)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignRS256 signs claims with an RS256 header using priv.
func SignRS256(priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	h := map[string]any{"alg": "RS256", "typ": "JWT"}
	if kid != "" {
		h["kid"] = kid
	}
	input := seg(h) + "." + seg(claims)
	sum := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// NoneToken forges an unsigned "alg":"none" token (for negative tests).
func NoneToken(claims map[string]any) string {
	h := map[string]any{"alg": "none", "typ": "JWT"}
	return seg(h) + "." + seg(claims) + "."
}

// HS256WithKid signs with an arbitrary kid so a caller can forge an alg-confusion token
// (HS256 header pointing at a kid the verifier holds as an RSA key).
func HS256WithKid(secret []byte, kid string, claims map[string]any) string {
	return SignHS256(secret, kid, claims)
}
