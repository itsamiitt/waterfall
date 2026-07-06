package security

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"testing"
)

// TestPBKDF2_SHA256KnownAnswerVectors runs the login KDF (PBKDF2-HMAC-SHA256 — the exact
// crypto/pbkdf2.Key(sha256.New, ...) call HashPassword/VerifyPassword derive with) against the
// widely-mirrored published PBKDF2-HMAC-SHA256 known-answer vectors (password "password", salt
// "salt"; the SHA-256 analogue of the RFC 6070 PBKDF2-HMAC-SHA1 set). Passing proves the derivation
// matches the standard rather than an incidental variant. All three dk values are standards-sourced.
func TestPBKDF2_SHA256KnownAnswerVectors(t *testing.T) {
	cases := []struct {
		password string
		salt     string
		iters    int
		dkLen    int
		wantHex  string // published PBKDF2-HMAC-SHA256 derived key
	}{
		{"password", "salt", 1, 32, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{"password", "salt", 2, 32, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{"password", "salt", 4096, 32, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, c := range cases {
		name := "c=" + strconv.Itoa(c.iters)
		t.Run(name, func(t *testing.T) {
			dk, err := pbkdf2.Key(sha256.New, c.password, []byte(c.salt), c.iters, c.dkLen)
			if err != nil {
				t.Fatalf("pbkdf2.Key: %v", err)
			}
			if got := hex.EncodeToString(dk); got != c.wantHex {
				t.Fatalf("PBKDF2-HMAC-SHA256(%q,%q,c=%d,dkLen=%d)\n got %s\nwant %s",
					c.password, c.salt, c.iters, c.dkLen, got, c.wantHex)
			}
		})
	}
}

// TestPBKDF2_RepoFormatMatchesKAT ties the KAT to the repo's own stored encoding: a
// pbkdf2-sha256$<iters>$<b64salt>$<b64dk> string built from a published known-answer vector must be
// accepted by VerifyPassword for the right password and rejected for the wrong one. This proves the
// pbkdf2Prefix encode/decode path and the constant-time compare agree with the standard KDF end to
// end — not just the raw derivation call above.
func TestPBKDF2_RepoFormatMatchesKAT(t *testing.T) {
	// KAT: password "password", salt "salt", c=2, dkLen=32.
	const (
		pw      = "password"
		iters   = 2
		wantHex = "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"
	)
	salt := []byte("salt")
	dk, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pbkdf2Prefix + "$" + strconv.Itoa(iters) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(dk)

	if !VerifyPassword(pw, encoded) {
		t.Errorf("VerifyPassword must accept a hash built from the PBKDF2-HMAC-SHA256 KAT: %q", encoded)
	}
	if VerifyPassword("not-the-password", encoded) {
		t.Error("VerifyPassword must reject the wrong password against the KAT-built hash")
	}
}
