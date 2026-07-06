package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"time"
)

// TOTP parameters (doc 05 §5.1): RFC 6238 with the HMAC-SHA1 default (authenticator-app
// compatibility), 6 digits, 30-second step, ±1 step acceptance window.
const (
	totpStep    = 30 * time.Second
	totpDigits  = 6
	totpSeedLen = 20 // RFC 6238 recommends a 160-bit SHA1 key
)

// NewSeed returns a fresh 20-byte TOTP seed from crypto/rand.
func NewSeed() []byte {
	b := make([]byte, totpSeedLen)
	_, _ = rand.Read(b)
	return b
}

// GenerateTOTP computes the RFC 6238 TOTP code for seed at time t (6 digits, SHA1, 30s step).
func GenerateTOTP(seed []byte, t time.Time) string {
	return hotp(seed, uint64(t.Unix())/uint64(totpStep.Seconds()))
}

// VerifyTOTP reports whether code is a valid TOTP for seed at time t, accepting the current step
// and ±1 step of clock skew. The compare is constant-time.
func VerifyTOTP(seed []byte, code string, t time.Time) bool {
	_, ok := verifyTOTPStep(seed, code, t)
	return ok
}

// verifyTOTPStep reports whether code is a valid TOTP for seed at time t within the ±1-step
// window and, when it is, the accepted time step (floor(unix/step)). It compares every window
// with no early return, so timing stays independent of which step matched — the same constant-time
// discipline as VerifyTOTP. The returned step is what the single-use replay guard records
// (mfa_used_steps, doc 05 §5.1 / OI-SEC-8), so a captured code cannot be replayed inside its window.
func verifyTOTPStep(seed []byte, code string, t time.Time) (step int64, ok bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	cur := int64(t.Unix()) / int64(totpStep.Seconds())
	matched := int64(0)
	found := false
	for _, w := range []int64{-1, 0, 1} {
		c := cur + w
		if c < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(seed, uint64(c))), []byte(code)) == 1 {
			matched = c
			found = true
		}
	}
	return matched, found
}

// hotp is the RFC 4226 HMAC-based one-time password with dynamic truncation, rendered to
// totpDigits decimal digits.
func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off]&0x7f) << 24) |
		(uint32(sum[off+1]) << 16) |
		(uint32(sum[off+2]) << 8) |
		uint32(sum[off+3])
	mod := uint32(1)
	for i := 0; i < totpDigits; i++ {
		mod *= 10
	}
	code := bin % mod
	s := strconv.FormatUint(uint64(code), 10)
	for len(s) < totpDigits {
		s = "0" + s
	}
	return s
}

// OTPAuthURL renders the otpauth:// provisioning URI the SPA turns into a QR code (doc 05 §5.2).
// The seed is base32-encoded (unpadded), the authenticator-standard form.
func OTPAuthURL(issuer, label string, seed []byte) string {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(totpDigits))
	q.Set("period", strconv.Itoa(int(totpStep.Seconds())))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+label) + "?" + q.Encode()
}
