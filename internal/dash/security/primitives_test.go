package security

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashVerifyRoundTrip(t *testing.T) {
	enc, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(enc, "pbkdf2-sha256$600000$") {
		t.Fatalf("unexpected encoding format: %q", enc)
	}
	if !VerifyPassword("correct horse battery staple", enc) {
		t.Error("correct password should verify")
	}
	if VerifyPassword("wrong password", enc) {
		t.Error("wrong password must not verify")
	}
}

func TestPasswordSaltIsUnique(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "pbkdf2-sha256$abc$x$y", "a$b$c$d$e"} {
		if VerifyPassword("x", bad) {
			t.Errorf("malformed encoding %q must not verify", bad)
		}
	}
}

func TestDummyHashRunsFullDerivation(t *testing.T) {
	if !strings.HasPrefix(DummyHash(), "pbkdf2-sha256$600000$") {
		t.Fatalf("DummyHash not a valid pbkdf2 encoding: %q", DummyHash())
	}
	if VerifyPassword("anything", DummyHash()) {
		t.Error("DummyHash must not verify a real password")
	}
}

// TestTOTP_RFC6238Vectors checks the 6-digit truncation of the RFC 6238 Appendix B SHA1 test
// vectors (seed = ASCII "12345678901234567890").
func TestTOTP_RFC6238Vectors(t *testing.T) {
	seed := []byte("12345678901234567890")
	cases := []struct {
		unix int64
		code string // last 6 digits of the 8-digit RFC value
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, c := range cases {
		got := GenerateTOTP(seed, time.Unix(c.unix, 0))
		if got != c.code {
			t.Errorf("GenerateTOTP(t=%d) = %q, want %q", c.unix, got, c.code)
		}
	}
}

func TestTOTP_VerifyWindow(t *testing.T) {
	seed := NewSeed()
	base := time.Unix(1_700_000_000, 0)
	code := GenerateTOTP(seed, base)
	// current step and ±1 step accepted
	for _, off := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		if !VerifyTOTP(seed, code, base.Add(off)) {
			t.Errorf("code should verify within ±1 step (offset %v)", off)
		}
	}
	// two steps away rejected
	if VerifyTOTP(seed, code, base.Add(90*time.Second)) {
		t.Error("code two steps away must be rejected")
	}
	if VerifyTOTP(seed, "000000", base) && GenerateTOTP(seed, base) != "000000" {
		t.Error("an unrelated code must not verify")
	}
}

func TestOTPAuthURL(t *testing.T) {
	seed := []byte("12345678901234567890")
	raw := OTPAuthURL("Waterfall", "ops@acme.example", seed)
	if !strings.HasPrefix(raw, "otpauth://totp/") {
		t.Fatalf("bad scheme: %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" {
		t.Errorf("unexpected params: %v", q)
	}
	want := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	if q.Get("secret") != want {
		t.Errorf("secret = %q, want %q", q.Get("secret"), want)
	}
}

func TestRecoveryCodes(t *testing.T) {
	plain, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if len(plain) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatalf("want %d codes, got %d/%d", RecoveryCodeCount, len(plain), len(hashes))
	}
	seen := map[string]bool{}
	for i, code := range plain {
		if len(code) != recoveryCodeLen {
			t.Errorf("code %q wrong length", code)
		}
		if seen[code] {
			t.Errorf("duplicate code %q", code)
		}
		seen[code] = true
		h := HashRecoveryCode(code)
		if string(h) != string(hashes[i]) {
			t.Errorf("hash mismatch for code %d", i)
		}
	}
}

func TestSplitCookie(t *testing.T) {
	tid, id, ok := splitCookie("acme|abc123")
	if !ok || tid != "acme" || id != "abc123" {
		t.Fatalf("splitCookie = %q,%q,%v", tid, id, ok)
	}
	for _, bad := range []string{"", "noPipe", "|id", "tenant|"} {
		if _, _, ok := splitCookie(bad); ok {
			t.Errorf("splitCookie(%q) should be !ok", bad)
		}
	}
}
