package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
)

func TestMemoryBackendSealOpenRoundTrip(t *testing.T) {
	b := NewMemoryBackend()
	ctx := context.Background()
	plaintext := []byte("hk_live_9a8b7c6d5e4f3a2b1c0d")

	id, err := b.Seal(ctx, "provider_key", plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := b.Open(ctx, id)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestMemoryBackendRotate(t *testing.T) {
	b := NewMemoryBackend()
	ctx := context.Background()
	plaintext := []byte("totp-seed-20-bytes!!")

	id, err := b.Seal(ctx, "totp_seed", plaintext)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	id2, err := b.Rotate(ctx, id)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if id2 == id {
		t.Fatal("Rotate returned the same id")
	}
	// both envelopes still open to the same plaintext (old is retained for retirement)
	for _, x := range []EnvelopeID{id, id2} {
		got, err := b.Open(ctx, x)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("Open(%s) = %q, %v", x, got, err)
		}
	}
	// lineage recorded on the successor
	if b.rows[id2].rotatedFrom != string(id) {
		t.Errorf("rotated_from = %q, want %q", b.rows[id2].rotatedFrom, id)
	}
}

func TestOpenNotFound(t *testing.T) {
	b := NewMemoryBackend()
	if _, err := b.Open(context.Background(), EnvelopeID("nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open(missing) = %v, want ErrNotFound", err)
	}
}

func TestUnknownKindRejected(t *testing.T) {
	b := NewMemoryBackend()
	if _, err := b.Seal(context.Background(), "not_a_kind", []byte("x")); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Seal(bad kind) = %v, want ErrUnknownKind", err)
	}
}

func TestAADBindsCiphertextToRow(t *testing.T) {
	kr := testKeyring(t)
	pepper := []byte("pepper")

	e, err := sealEnvelope(kr, pepper, "provider_key", []byte("secret"), "")
	if err != nil {
		t.Fatalf("sealEnvelope: %v", err)
	}
	// sanity: unmodified opens
	if _, err := openEnvelope(kr, e); err != nil {
		t.Fatalf("openEnvelope(clean): %v", err)
	}
	// tamper the id: AAD = id||kind changes, GCM authentication must fail
	swapped := e
	swapped.id = EnvelopeID("00000000-0000-4000-8000-000000000000")
	if _, err := openEnvelope(kr, swapped); !errors.Is(err, ErrCorruptEnvelope) {
		t.Errorf("open after id swap = %v, want ErrCorruptEnvelope", err)
	}
	// tamper the kind: same AAD binding
	swapped2 := e
	swapped2.kind = "totp_seed"
	if _, err := openEnvelope(kr, swapped2); !errors.Is(err, ErrCorruptEnvelope) {
		t.Errorf("open after kind swap = %v, want ErrCorruptEnvelope", err)
	}
	// tamper a ciphertext byte
	swapped3 := e
	swapped3.ciphertext = append([]byte(nil), e.ciphertext...)
	swapped3.ciphertext[0] ^= 0xff
	if _, err := openEnvelope(kr, swapped3); !errors.Is(err, ErrCorruptEnvelope) {
		t.Errorf("open after ciphertext flip = %v, want ErrCorruptEnvelope", err)
	}
}

func TestFingerprintOnlyForProviderKey(t *testing.T) {
	kr := testKeyring(t)
	pepper := []byte("pepper")

	pk, err := sealEnvelope(kr, pepper, "provider_key", []byte("k"), "")
	if err != nil {
		t.Fatal(err)
	}
	if pk.fingerprint == nil {
		t.Error("provider_key envelope missing fingerprint")
	}
	seed, err := sealEnvelope(kr, pepper, "totp_seed", []byte("k"), "")
	if err != nil {
		t.Fatal(err)
	}
	if seed.fingerprint != nil {
		t.Error("totp_seed envelope should have no fingerprint")
	}
}

func TestSecretRedaction(t *testing.T) {
	s := NewSecret([]byte("hk_live_supersecret"))
	if s.String() != "[REDACTED]" {
		t.Errorf("String() = %q", s.String())
	}
	if got := s.GoString(); got != "[REDACTED]" {
		t.Errorf("GoString() = %q", got)
	}
	j, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(j) != `"[REDACTED]"` {
		t.Errorf("MarshalJSON = %s", j)
	}
	// embedded in a struct — must not leak
	wrapper := struct {
		Name string `json:"name"`
		Key  Secret `json:"key"`
	}{Name: "hunter", Key: s}
	jb, _ := json.Marshal(wrapper)
	if bytes.Contains(jb, []byte("supersecret")) {
		t.Errorf("plaintext leaked through JSON: %s", jb)
	}
	// and still reachable at the point of use
	if !bytes.Equal(s.Bytes(), []byte("hk_live_supersecret")) {
		t.Error("Bytes() should return the plaintext")
	}
}

// TestPepperRotationRefingerprint proves the OI-SEC-4 pepper-rotation seam: a key re-fingerprinted
// after a pepper change gets a fresh fingerprint under the NEW pepper, while an untouched envelope
// keeps its OLD-pepper fingerprint — so cross-pepper duplicate detection degrades gracefully rather
// than breaking.
func TestPepperRotationRefingerprint(t *testing.T) {
	ctx := context.Background()
	b := NewMemoryBackend()
	oldPepper := append([]byte(nil), b.pepper...)
	pt := []byte("hk_live_rotate_me")

	// Seal two provider keys under the original pepper.
	rotated, err := b.Seal(ctx, "provider_key", pt)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	untouched, err := b.Seal(ctx, "provider_key", []byte("hk_live_other"))
	if err != nil {
		t.Fatalf("Seal other: %v", err)
	}
	fpOld := append([]byte(nil), b.rows[rotated].fingerprint...)
	if !bytes.Equal(fpOld, Fingerprint(oldPepper, pt)) {
		t.Fatal("initial fingerprint should be HMAC(oldPepper, plaintext)")
	}
	untouchedFP := append([]byte(nil), b.rows[untouched].fingerprint...)

	// Operator rotates the pepper: the backend is now keyed by a new pepper.
	newPepper := make([]byte, 32)
	if _, err := rand.Read(newPepper); err != nil {
		t.Fatal(err)
	}
	b.pepper = newPepper

	// Re-fingerprint ONLY the rotated key (plaintext in hand at rotate/import).
	if err := b.RefingerprintOnRotate(ctx, rotated, pt); err != nil {
		t.Fatalf("RefingerprintOnRotate: %v", err)
	}
	fpNew := b.rows[rotated].fingerprint
	if bytes.Equal(fpNew, fpOld) {
		t.Fatal("fingerprint must change after re-fingerprinting under the new pepper")
	}
	if !bytes.Equal(fpNew, Fingerprint(newPepper, pt)) {
		t.Fatal("re-fingerprint must be HMAC(newPepper, plaintext)")
	}
	// The untouched envelope still carries its OLD-pepper fingerprint (graceful degradation).
	if !bytes.Equal(b.rows[untouched].fingerprint, untouchedFP) {
		t.Fatal("an un-rotated envelope must keep its old-pepper fingerprint")
	}
	// Confidentiality is unaffected by the pepper change: both still open to their plaintext.
	if got, err := b.Open(ctx, rotated); err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("Open(rotated) after pepper change = %q, %v", got, err)
	}

	// Refingerprint is a no-op for non-provider_key kinds and ErrNotFound for unknown ids.
	seedID, err := b.Seal(ctx, "totp_seed", []byte("seed-plaintext-20by"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RefingerprintOnRotate(ctx, seedID, []byte("seed-plaintext-20by")); err != nil {
		t.Fatalf("refingerprint totp_seed should be a no-op, got %v", err)
	}
	if b.rows[seedID].fingerprint != nil {
		t.Fatal("totp_seed must never acquire a fingerprint")
	}
	if err := b.RefingerprintOnRotate(ctx, EnvelopeID("nope"), pt); !errors.Is(err, ErrNotFound) {
		t.Errorf("refingerprint unknown id = %v, want ErrNotFound", err)
	}
}

func TestKeyringParse(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	for i := range k1 {
		k1[i] = byte(i)
		k2[i] = byte(255 - i)
	}
	env := base64.StdEncoding.EncodeToString(k1) + " , " + base64.StdEncoding.EncodeToString(k2)
	kr, err := NewKeyring(env)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	activeID, activeKEK := kr.Active()
	if activeID != keyID(k1) {
		t.Errorf("active id = %q, want first entry %q", activeID, keyID(k1))
	}
	if !bytes.Equal(activeKEK, k1) {
		t.Error("active KEK is not the first entry")
	}
	if _, ok := kr.Get(keyID(k2)); !ok {
		t.Error("second key not retained for unwrap")
	}
	if len(activeID) != 8 {
		t.Errorf("id should be 8 hex chars, got %q", activeID)
	}
}

func TestKeyringErrors(t *testing.T) {
	if _, err := NewKeyring(""); !errors.Is(err, ErrNoMasterKey) {
		t.Errorf("empty env = %v, want ErrNoMasterKey", err)
	}
	short := base64.StdEncoding.EncodeToString([]byte("too-short"))
	if _, err := NewKeyring(short); !errors.Is(err, ErrKeySize) {
		t.Errorf("short key = %v, want ErrKeySize", err)
	}
	if _, err := NewKeyringFromKeys("missing", map[string][]byte{"a": make([]byte, 32)}); !errors.Is(err, ErrUnknownKeyID) {
		t.Errorf("bad active = %v, want ErrUnknownKeyID", err)
	}
}

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u, err := uuidV4()
		if err != nil {
			t.Fatal(err)
		}
		if !uuidV4Re.MatchString(u) {
			t.Fatalf("uuid %q is not a valid v4", u)
		}
		if seen[u] {
			t.Fatalf("duplicate uuid %q", u)
		}
		seen[u] = true
	}
}

func TestGCMSelfConsistency(t *testing.T) {
	key := make([]byte, 32)
	nonce := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(nonce)
	pt := []byte("the quick brown fox")
	ad := []byte("aad")

	ct, err := gcmSeal(key, nonce, pt, ad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := gcmOpen(key, nonce, ct, ad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("gcm round-trip mismatch")
	}
	// wrong AAD must fail
	if _, err := gcmOpen(key, nonce, ct, []byte("other")); !errors.Is(err, ErrCorruptEnvelope) {
		t.Errorf("gcmOpen wrong AAD = %v, want ErrCorruptEnvelope", err)
	}
}

func TestByteaRoundTrip(t *testing.T) {
	in := []byte{0x00, 0x01, 0xde, 0xad, 0xbe, 0xef, 0xff}
	enc := encodeBytea(in)
	if enc[:2] != `\x` {
		t.Fatalf("encodeBytea prefix = %q", enc[:2])
	}
	out, err := decodeBytea(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("bytea round-trip: got %x want %x", out, in)
	}
	if _, err := decodeBytea("deadbeef"); !errors.Is(err, ErrCorruptEnvelope) {
		t.Errorf("decodeBytea(no prefix) = %v, want ErrCorruptEnvelope", err)
	}
}

func testKeyring(t *testing.T) *Keyring {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	id := keyID(key)
	kr, err := NewKeyringFromKeys(id, map[string][]byte{id: key})
	if err != nil {
		t.Fatal(err)
	}
	return kr
}
