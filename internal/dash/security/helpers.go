package security

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/enrichment/waterfall/internal/dash/rbac"
)

// Sentinel errors. None carries secret material or discloses cross-tenant existence.
var (
	// ErrSession reports a missing, malformed, revoked, or expired session — always mapped to a
	// uniform 401 by the HTTP layer, never distinguished on the wire.
	ErrSession = errors.New("security: session not found or expired")
	// ErrNotFound reports an absent user or row (mapped to 404, existence never disclosed).
	ErrNotFound = errors.New("security: not found")
	// ErrMFANotEnrolled reports a TOTP operation on a user with no sealed seed.
	ErrMFANotEnrolled = errors.New("security: mfa not enrolled")
	// ErrBadCode reports a TOTP or recovery code that did not verify.
	ErrBadCode = errors.New("security: invalid code")
)

// RequiresMFA reports whether a role must complete MFA before it can authenticate for anything
// beyond the enrollment endpoints (doc 05 §5.3: operator and tenant_admin are mandatory).
func RequiresMFA(role string) bool {
	return role == rbac.RoleOperator || role == rbac.RoleTenantAdmin
}

// newUUID hand-rolls an RFC 4122 v4 uuid from crypto/rand (stdlib only), matching the format the
// dashboard schema uses for uuid primary keys.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// randID returns nbytes of crypto/rand as a base64url (unpadded) string — used for the 256-bit
// session id and the 128-bit CSRF token (doc 05 §4.1).
func randID(nbytes int) string {
	b := make([]byte, nbytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewSeedString returns a random 24-byte base64url string, used as an unguessable placeholder
// password for invited/reset users (the real password is set through the reset flow).
func NewSeedString() string { return randID(24) }

// encodeBytea / decodeBytea round-trip a bytea column through \x hex text, since the internal/pg
// client sends parameters in text format and has no []byte encoder (mirrors internal/dash/secrets
// and internal/dash/audit).
func encodeBytea(b []byte) string { return `\x` + hex.EncodeToString(b) }

func decodeBytea(s string) ([]byte, error) {
	if len(s) >= 2 && s[0] == '\\' && (s[1] == 'x' || s[1] == 'X') {
		return hex.DecodeString(s[2:])
	}
	return nil, fmt.Errorf("security: bytea not in \\x hex form")
}

// str dereferences a nullable text column to "" on NULL.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// parseTS parses a Postgres timestamptz text rendering (or RFC3339) into a UTC time.Time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
