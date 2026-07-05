// Package security implements the dashboard's identity primitives (doc 05 §4–§6): PBKDF2
// password hashing, RFC 6238 TOTP, single-use recovery codes, plus the tenant-scoped services
// over internal/dash/db that persist users, browser sessions, IP allowlists, and the async
// api_access_log. Every persistence method derives tenant_id and role ONLY from the verified
// Principal carried in the context (gate G1) — never from a caller-supplied argument.
//
// The pure primitives (password/totp/recovery) are stdlib-only and fully unit-tested offline;
// the stores are exercised end-to-end against real Postgres in the integration suite.
package security

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
)

// PBKDF2 parameters (doc 05 §4.3): PBKDF2-HMAC-SHA256, 600k iterations, 16-byte random salt,
// 32-byte derived key. The stored form is pbkdf2-sha256$<iters>$<b64salt>$<b64dk>.
const (
	pbkdf2Iters   = 600000
	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 32
	pbkdf2Prefix  = "pbkdf2-sha256"
)

// HashPassword derives and encodes a new password hash. The salt is fresh per call.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iters, pbkdf2KeyLen)
	if err != nil {
		return "", err
	}
	return pbkdf2Prefix + "$" + strconv.Itoa(pbkdf2Iters) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(dk), nil
}

// VerifyPassword reports whether pw matches the encoded pbkdf2-sha256 hash. The final compare is
// constant-time; a malformed encoding returns false without panicking. It always runs the full
// key derivation so callers can equalize timing against DummyHash for unknown emails.
func VerifyPassword(pw, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Prefix {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iters, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is a fixed valid encoded hash used to equalize login response timing for unknown
// emails, so a caller cannot distinguish "no such user" from "wrong password" by latency
// (doc 05 §4.3). Computed once (600k iterations) on first use.
var (
	dummyOnce sync.Once
	dummyHash string
)

// DummyHash returns the timing-equalization hash. Run VerifyPassword against it on the
// unknown-email path and discard the result.
func DummyHash() string {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("dummy-password-for-constant-time-login") })
	return dummyHash
}
