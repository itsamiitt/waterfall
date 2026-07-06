package provisioning

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// newInviteToken mints a one-time invite token for tenantID. The plaintext form is
// "<tenant_id>|<base64url(256-bit secret)>": the tenant prefix is a non-secret routing hint (it
// lets the pre-session accept path bind app.current_tenant before RLS discloses the row, per doc
// 05 SEC-6), and the 256-bit secret is the sole authenticator. Only sha256(plaintext) is ever
// persisted (never the plaintext). The plaintext is returned to the caller exactly once.
func newInviteToken(tenantID string) (plaintext string, hash []byte, err error) {
	b := make([]byte, inviteRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plaintext = tenantID + "|" + base64.RawURLEncoding.EncodeToString(b)
	return plaintext, hashToken(plaintext), nil
}

// hashToken returns sha256 of the full plaintext token — the exact bytes stored in
// tenant_invites.token_hash, so the accept path looks the invite up by hashing the token it
// receives (never the plaintext) under the invite's own Tenant binding.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// splitToken extracts the Tenant routing hint from an invite token. It returns ok=false for a
// malformed token (no separator, or an empty prefix/secret) so the accept path fails closed.
func splitToken(token string) (tenantID, secret string, ok bool) {
	i := strings.IndexByte(token, '|')
	if i <= 0 || i == len(token)-1 {
		return "", "", false
	}
	return token[:i], token[i+1:], true
}

// encodeBytea renders a byte slice as Postgres \x hex text (the internal/pg client sends
// parameters in text format and has no []byte encoder; cast with ::bytea in the SQL).
func encodeBytea(b []byte) string { return `\x` + hex.EncodeToString(b) }

// newUUID hand-rolls an RFC 4122 v4 uuid from crypto/rand (stdlib only), matching the uuid PK
// format the dashboard schema uses.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// validEmail is a deliberately minimal syntactic check (there is no email-delivery dependency on
// this surface): exactly one '@', with a non-empty local part and a dotted domain.
func validEmail(e string) bool {
	at := strings.IndexByte(e, '@')
	if at <= 0 || at != strings.LastIndexByte(e, '@') || at == len(e)-1 {
		return false
	}
	domain := e[at+1:]
	dot := strings.IndexByte(domain, '.')
	return dot > 0 && dot < len(domain)-1
}
