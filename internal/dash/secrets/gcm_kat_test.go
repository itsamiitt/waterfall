package secrets

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// gcmKAT is one vendored AES-256-GCM known-answer vector (see testdata/aes256gcm_nist.json).
type gcmKAT struct {
	Name string `json:"name"`
	NIST bool   `json:"nist"`
	Key  string `json:"key"`
	IV   string `json:"iv"`
	AAD  string `json:"aad"`
	PT   string `json:"pt"`
	CT   string `json:"ct"`
	Tag  string `json:"tag"`
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestGCM_NISTKnownAnswerVectors runs the package's own AES-256-GCM primitive (gcmSeal/gcmOpen,
// the exact seam the envelope Seal/Open path uses via newGCM -> cipher.NewGCM over aes-256)
// against the NIST-published GCM known-answer vectors in testdata/aes256gcm_nist.json (GCM spec /
// SP 800-38D test cases 13-16; 256-bit key, 96-bit IV, 128-bit tag). Passing proves the envelope
// crypto is standards-correct GCM: encrypt reproduces the published ciphertext+tag exactly, and
// decrypt recovers the plaintext. All four vectors are standards-sourced (marked nist:true), not
// self-consistent round-trips.
func TestGCM_NISTKnownAnswerVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/aes256gcm_nist.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var doc struct {
		Vectors []gcmKAT `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatal("no vectors loaded")
	}

	for _, v := range doc.Vectors {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			key := mustHex(t, v.Key)
			if len(key) != 32 {
				t.Fatalf("vector %s: key is %d bytes, want 32 (AES-256)", v.Name, len(key))
			}
			nonce := mustHex(t, v.IV)
			if len(nonce) != 12 {
				t.Fatalf("vector %s: iv is %d bytes, want 12 (96-bit)", v.Name, len(nonce))
			}
			var aad []byte
			if v.AAD != "" {
				aad = mustHex(t, v.AAD)
			}
			pt := mustHex(t, v.PT)
			wantCT := mustHex(t, v.CT)
			wantTag := mustHex(t, v.Tag)
			// Go's AEAD Seal returns ciphertext||tag; the NIST vector splits the two.
			want := append(append([]byte{}, wantCT...), wantTag...)

			// Encrypt KAT: the package primitive must reproduce the published ciphertext+tag byte-for-byte.
			got, err := gcmSeal(key, nonce, pt, aad)
			if err != nil {
				t.Fatalf("gcmSeal: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("encrypt mismatch\n got %x\nwant %x", got, want)
			}

			// Decrypt KAT: opening the published ciphertext+tag must recover the plaintext.
			back, err := gcmOpen(key, nonce, want, aad)
			if err != nil {
				t.Fatalf("gcmOpen(valid): %v", err)
			}
			if !bytes.Equal(back, pt) {
				t.Fatalf("decrypt mismatch\n got %x\nwant %x", back, pt)
			}
		})
	}
}

// TestGCM_CorruptTagFailsDecrypt is the decrypt tag-fail known-answer: a single flipped bit in the
// authentication tag of a valid NIST vector must make Open fail closed (ErrCorruptEnvelope), never
// returning plaintext. This is the property the envelope seam relies on to reject a tampered or
// spliced secret_envelopes row.
func TestGCM_CorruptTagFailsDecrypt(t *testing.T) {
	// TC14: zero key / zero IV / one plaintext block (GCM spec case 14).
	key := mustHex(t, "0000000000000000000000000000000000000000000000000000000000000000")
	nonce := mustHex(t, "000000000000000000000000")
	ct := mustHex(t, "cea7403d4d606b6e074ec5d3baf39d18")
	tag := mustHex(t, "d0d1c8a799996bf0265b98b5d48ab919")

	sealed := append(append([]byte{}, ct...), tag...)

	// Sanity: the untampered vector opens.
	if _, err := gcmOpen(key, nonce, sealed, nil); err != nil {
		t.Fatalf("gcmOpen(valid TC14): %v", err)
	}

	// Flip one bit of the tag (the last byte) -> authentication must fail.
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := gcmOpen(key, nonce, tampered, nil); !errors.Is(err, ErrCorruptEnvelope) {
		t.Fatalf("gcmOpen(corrupt tag) = %v, want ErrCorruptEnvelope", err)
	}

	// A truncated tag (short input) must also fail closed, not panic.
	if _, err := gcmOpen(key, nonce, sealed[:len(sealed)-4], nil); err == nil {
		t.Fatal("gcmOpen(truncated) unexpectedly succeeded")
	}
}
