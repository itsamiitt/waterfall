// Package auth verifies signed JWTs (RFC 7519 / JWS RFC 7515) into a set of claims the API
// turns into a tenant Principal (gate G1). It is stdlib-only and supports HS256 and RS256
// with key rotation by `kid`.
//
// Security posture (docs/18 §1):
//   - The verification algorithm is pinned by the KEY, never chosen by the token header.
//     This defeats the classic alg-confusion attack (a token that flips RS256→HS256 so the
//     RSA public key is used as an HMAC secret) and `alg: "none"`.
//   - `exp` is REQUIRED; `iss`/`aud` must match the configured expectation; a small clock
//     leeway absorbs skew.
//   - HMAC comparison is constant-time.
//   - `tenant_id` is REQUIRED and non-empty — a token without it is rejected, so G1 can
//     never fall back to an ambient/empty tenant.
package auth

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors (all map to 401 at the edge, except missing scope which is authorization).
var (
	ErrMalformed      = errors.New("auth: malformed token")
	ErrUnsupportedAlg = errors.New("auth: unsupported or disallowed algorithm")
	ErrUnknownKID     = errors.New("auth: unknown key id")
	ErrBadSignature   = errors.New("auth: signature verification failed")
	ErrExpired        = errors.New("auth: token expired")
	ErrNotYetValid    = errors.New("auth: token not yet valid")
	ErrIssuer         = errors.New("auth: issuer not accepted")
	ErrAudience       = errors.New("auth: audience not accepted")
	ErrNoTenant       = errors.New("auth: token has no tenant_id claim")
)

// Claims are the validated contents the API cares about.
type Claims struct {
	Subject   string
	TenantID  string
	Scopes    []string
	Issuer    string
	Audience  string
	ExpiresAt int64
	IssuedAt  int64
	NotBefore int64
}

type key struct {
	alg    string
	hmac   []byte
	rsaPub *rsa.PublicKey
}

// Verifier holds the trusted keys and the expected issuer/audience.
type Verifier struct {
	keys   map[string]key
	iss    string
	aud    string
	leeway time.Duration
	now    func() time.Time
}

// Option configures a Verifier.
type Option func(*Verifier)

// WithIssuer requires tokens to carry iss == want.
func WithIssuer(want string) Option { return func(v *Verifier) { v.iss = want } }

// WithAudience requires tokens to carry (or include) aud == want.
func WithAudience(want string) Option { return func(v *Verifier) { v.aud = want } }

// WithLeeway sets the allowed clock skew for exp/nbf (default 60s).
func WithLeeway(d time.Duration) Option { return func(v *Verifier) { v.leeway = d } }

// WithClock injects the time source (tests).
func WithClock(now func() time.Time) Option { return func(v *Verifier) { v.now = now } }

// NewVerifier builds a Verifier. Add at least one key before use.
func NewVerifier(opts ...Option) *Verifier {
	v := &Verifier{keys: map[string]key{}, leeway: 60 * time.Second, now: time.Now}
	for _, o := range opts {
		o(v)
	}
	return v
}

// AddHMACKey registers an HS256 verification key under kid. Rotation = register the new kid
// alongside the old; tokens select by their header `kid`.
func (v *Verifier) AddHMACKey(kid string, secret []byte) {
	cp := append([]byte(nil), secret...)
	v.keys[kid] = key{alg: "HS256", hmac: cp}
}

// AddRSAKey registers an RS256 verification (public) key under kid.
func (v *Verifier) AddRSAKey(kid string, pub *rsa.PublicKey) {
	v.keys[kid] = key{alg: "RS256", rsaPub: pub}
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// rawClaims mirrors the JSON; aud may be a string or an array, so it is decoded loosely.
type rawClaims struct {
	Sub      string          `json:"sub"`
	Iss      string          `json:"iss"`
	Aud      json.RawMessage `json:"aud"`
	Exp      *int64          `json:"exp"`
	Nbf      *int64          `json:"nbf"`
	Iat      *int64          `json:"iat"`
	TenantID string          `json:"tenant_id"`
	Scope    string          `json:"scope"`  // space-delimited (OAuth2)
	Scopes   []string        `json:"scopes"` // array form
}

// Verify parses and fully validates a compact JWS, returning its claims.
func (v *Verifier) Verify(token string) (Claims, error) {
	h, hb, pb, sig, err := split(token)
	if err != nil {
		return Claims{}, err
	}

	// Reject "none" and anything not explicitly allowed. The KEY's alg — not the header —
	// decides how we verify, so a mismatched header cannot force a weaker path.
	if h.Alg == "none" || h.Alg == "" {
		return Claims{}, ErrUnsupportedAlg
	}
	k, ok := v.keys[h.Kid]
	if !ok {
		return Claims{}, ErrUnknownKID
	}
	if h.Alg != k.alg {
		// e.g. token says HS256 but the kid is an RSA key: alg-confusion attempt.
		return Claims{}, ErrUnsupportedAlg
	}

	signingInput := []byte(hb + "." + pb)
	switch k.alg {
	case "HS256":
		mac := hmac.New(sha256.New, k.hmac)
		mac.Write(signingInput)
		if !hmac.Equal(sig, mac.Sum(nil)) {
			return Claims{}, ErrBadSignature
		}
	case "RS256":
		sum := sha256.Sum256(signingInput)
		if err := rsa.VerifyPKCS1v15(k.rsaPub, crypto.SHA256, sum[:], sig); err != nil {
			return Claims{}, ErrBadSignature
		}
	default:
		return Claims{}, ErrUnsupportedAlg
	}

	var rc rawClaims
	if err := json.Unmarshal(pb2bytes(pb), &rc); err != nil {
		return Claims{}, ErrMalformed
	}
	return v.validateClaims(rc)
}

func (v *Verifier) validateClaims(rc rawClaims) (Claims, error) {
	now := v.now()
	if rc.Exp == nil {
		return Claims{}, ErrExpired // exp is required
	}
	if now.After(time.Unix(*rc.Exp, 0).Add(v.leeway)) {
		return Claims{}, ErrExpired
	}
	if rc.Nbf != nil && now.Add(v.leeway).Before(time.Unix(*rc.Nbf, 0)) {
		return Claims{}, ErrNotYetValid
	}
	if v.iss != "" && rc.Iss != v.iss {
		return Claims{}, ErrIssuer
	}
	if v.aud != "" {
		if !audienceMatches(rc.Aud, v.aud) {
			return Claims{}, ErrAudience
		}
	}
	if rc.TenantID == "" {
		return Claims{}, ErrNoTenant
	}

	c := Claims{
		Subject:  rc.Sub,
		TenantID: rc.TenantID,
		Issuer:   rc.Iss,
		Scopes:   mergeScopes(rc.Scope, rc.Scopes),
	}
	if rc.Exp != nil {
		c.ExpiresAt = *rc.Exp
	}
	if rc.Iat != nil {
		c.IssuedAt = *rc.Iat
	}
	if rc.Nbf != nil {
		c.NotBefore = *rc.Nbf
	}
	return c, nil
}

// --- helpers ---

func split(token string) (header, string, string, []byte, error) {
	var h header
	var hb, pb string
	first := -1
	second := -1
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			if first < 0 {
				first = i
			} else if second < 0 {
				second = i
			} else {
				return h, "", "", nil, ErrMalformed // more than 2 dots
			}
		}
	}
	// header and payload must be non-empty; an empty signature segment is allowed through so
	// the alg check can reject "none" explicitly (and HS256/RS256 fail as a bad signature).
	if first < 0 || second < 0 || first == 0 || second == first+1 {
		return h, "", "", nil, ErrMalformed
	}
	hb = token[:first]
	pb = token[first+1 : second]
	sb := token[second+1:]

	hraw, err := decodeSegment(hb)
	if err != nil {
		return h, "", "", nil, ErrMalformed
	}
	if err := json.Unmarshal(hraw, &h); err != nil {
		return h, "", "", nil, ErrMalformed
	}
	sig, err := decodeSegment(sb)
	if err != nil {
		return h, "", "", nil, ErrMalformed
	}
	return h, hb, pb, sig, nil
}

// pb2bytes decodes the payload segment (already known well-formed enough to have split).
func pb2bytes(pb string) []byte {
	b, err := decodeSegment(pb)
	if err != nil {
		return []byte("{}")
	}
	return b
}

func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func audienceMatches(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	// aud may be a JSON string or an array of strings.
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func mergeScopes(spaceDelim string, arr []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// split spaceDelim on spaces without strings.Fields to keep it explicit
	start := -1
	for i := 0; i <= len(spaceDelim); i++ {
		if i == len(spaceDelim) || spaceDelim[i] == ' ' {
			if start >= 0 {
				add(spaceDelim[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	for _, s := range arr {
		add(s)
	}
	return out
}

// Describe is a small helper for logs (never logs the token itself).
func Describe(c Claims) string {
	return fmt.Sprintf("tenant=%s sub=%s scopes=%v", c.TenantID, c.Subject, c.Scopes)
}
