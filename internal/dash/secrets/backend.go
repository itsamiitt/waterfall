// Package secrets is the dashboard's envelope-encryption seam (ADR-0017, doc 05 §7): one
// sealed store, secret_envelopes, custodies every secret the dashboard holds (Provider Key
// material, TOTP seeds, webhook secrets, alert channel configs) with a per-secret AES-256-GCM
// DEK wrapped by a master KEK.
//
// Gates / invariants enforced:
//   - Confidentiality at rest. No plaintext column exists; Seal generates a fresh 32-byte DEK,
//     GCM-encrypts the plaintext with AAD = envelope_id || kind (so a spliced or swapped row
//     fails authentication on Open), wraps the DEK under the active KEK, and for provider_key
//     also records a KEYED HMAC-SHA256(pepper, plaintext) duplicate-detection fingerprint.
//   - G1 tenant isolation. secret_envelopes is a Class-P table; PGBackend reads and writes it
//     ONLY through db.Store.PlatformTx (tenant='platform'), the one-owner-per-table path.
//   - No leak to logs/JSON. The Secret wrapper redacts String() and MarshalJSON().
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// EnvelopeID is the primary key of a secret_envelopes row (a v4 uuid string).
type EnvelopeID string

// Backend seals, opens, and rotates secrets. Implementations must never return or log
// plaintext except through Open's []byte, which callers wrap in Secret and zero when done.
type Backend interface {
	// Seal encrypts plaintext under a fresh DEK and returns the new envelope id.
	Seal(ctx context.Context, kind string, plaintext []byte) (EnvelopeID, error)
	// Open decrypts and returns the plaintext for id.
	Open(ctx context.Context, id EnvelopeID) ([]byte, error)
	// Rotate re-seals id's plaintext under a new envelope (rotated_from lineage) and returns
	// the successor id. The old envelope is left intact for the caller to retire.
	Rotate(ctx context.Context, id EnvelopeID) (EnvelopeID, error)
}

// Sentinel errors (wrapped with %w by callers). None carries secret material.
var (
	// ErrNotFound reports that no envelope exists for the requested id.
	ErrNotFound = errors.New("secrets: envelope not found")
	// ErrUnknownKind reports a kind outside the secret_envelopes CHECK enum.
	ErrUnknownKind = errors.New("secrets: unknown envelope kind")
	// ErrCorruptEnvelope reports a stored envelope that fails structural or authentication checks.
	ErrCorruptEnvelope = errors.New("secrets: corrupt or unauthenticated envelope")
)

// validKinds is the secret_envelopes.kind CHECK enum (migration 0004).
var validKinds = map[string]bool{
	"provider_key":   true,
	"totp_seed":      true,
	"webhook_secret": true,
	"channel_config": true,
}

// envelope is the in-memory image of a secret_envelopes row.
type envelope struct {
	id          EnvelopeID
	kind        string
	masterKeyID string
	dekWrapped  []byte
	nonce       []byte
	ciphertext  []byte
	fingerprint []byte // nil unless kind == "provider_key"
	rotatedFrom string // "" when this is an original seal
}

// sealEnvelope builds a fully-encrypted envelope for plaintext. It is the shared core of both
// the in-memory and Postgres backends, so their crypto is byte-for-byte identical. rotatedFrom
// is "" for an original seal, or the predecessor id when re-sealing under Rotate.
func sealEnvelope(kr *Keyring, pepper []byte, kind string, plaintext []byte, rotatedFrom string) (envelope, error) {
	if !validKinds[kind] {
		return envelope{}, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	idStr, err := uuidV4()
	if err != nil {
		return envelope{}, err
	}
	id := EnvelopeID(idStr)

	dek := make([]byte, 32) // one fresh DEK per secret, never reused
	if _, err := rand.Read(dek); err != nil {
		return envelope{}, err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return envelope{}, err
	}
	ct, err := gcmSeal(dek, nonce, plaintext, aad(id, kind))
	if err != nil {
		return envelope{}, err
	}
	activeID, kek := kr.Active()
	wrapped, err := wrapDEK(kek, dek)
	if err != nil {
		return envelope{}, err
	}
	e := envelope{
		id:          id,
		kind:        kind,
		masterKeyID: activeID,
		dekWrapped:  wrapped,
		nonce:       nonce,
		ciphertext:  ct,
		rotatedFrom: rotatedFrom,
	}
	if kind == "provider_key" {
		e.fingerprint = fingerprintOf(pepper, plaintext)
	}
	return e, nil
}

// openEnvelope decrypts e's plaintext: unwrap the DEK with the recorded master key, then
// GCM-open the ciphertext under AAD = id || kind. A tampered or mis-keyed row fails
// authentication and returns ErrCorruptEnvelope.
func openEnvelope(kr *Keyring, e envelope) ([]byte, error) {
	kek, ok := kr.Get(e.masterKeyID)
	if !ok {
		return nil, fmt.Errorf("%w: master key %q", ErrUnknownKeyID, e.masterKeyID)
	}
	dek, err := unwrapDEK(kek, e.dekWrapped)
	if err != nil {
		return nil, err
	}
	return gcmOpen(dek, e.nonce, e.ciphertext, aad(e.id, e.kind))
}

// aad binds a ciphertext to its own row: AAD = envelope_id || kind (doc 05 §7.1).
func aad(id EnvelopeID, kind string) []byte {
	out := make([]byte, 0, len(id)+len(kind))
	out = append(out, id...)
	out = append(out, kind...)
	return out
}

// uuidV4 hand-rolls an RFC 4122 version-4 uuid from crypto/rand (stdlib only).
func uuidV4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// gcmSeal / gcmOpen are AES-256-GCM (12-byte nonce, 16-byte tag) with additional data.
func gcmSeal(key, nonce, plaintext, ad []byte) ([]byte, error) {
	g, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return g.Seal(nil, nonce, plaintext, ad), nil
}

func gcmOpen(key, nonce, ciphertext, ad []byte) ([]byte, error) {
	g, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return nil, ErrCorruptEnvelope // authentication failure — never surface crypto internals
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key) // 32-byte key => AES-256
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

// wrapDEK returns wrap_nonce || AES-256-GCM(KEK, wrap_nonce, dek) (doc 05 §7.1 step 3).
func wrapDEK(kek, dek []byte) ([]byte, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct, err := gcmSeal(kek, nonce, dek, nil)
	if err != nil {
		return nil, err
	}
	return append(nonce, ct...), nil
}

// unwrapDEK reverses wrapDEK.
func unwrapDEK(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped) < 12 {
		return nil, ErrCorruptEnvelope
	}
	return gcmOpen(kek, wrapped[:12], wrapped[12:], nil)
}

// fingerprintOf is the keyed duplicate-detection fingerprint for provider_key material
// (HMAC-SHA256 — never bare SHA-256, so a leaked row cannot be brute-forced, doc 05 §7.1).
func fingerprintOf(pepper, plaintext []byte) []byte {
	m := hmac.New(sha256.New, pepper)
	m.Write(plaintext)
	return m.Sum(nil)
}

// Fingerprint returns the keyed duplicate-detection fingerprint HMAC-SHA256(pepper, plaintext) for
// provider_key material (doc 05 §7.1). It is exported so the key rotate/import path can compute a
// candidate's fingerprint under a chosen pepper — the one moment plaintext is legitimately in hand
// — to compare against stored fingerprints, without exposing the pepper itself.
func Fingerprint(pepper, plaintext []byte) []byte { return fingerprintOf(pepper, plaintext) }

// Refingerprinter is the optional pepper-rotation seam (OI-SEC-4). Backends that can migrate a
// single envelope's duplicate-detection fingerprint to the current pepper in place implement it;
// both PGBackend and MemoryBackend do. It is deliberately NOT part of Backend (that interface stays
// the ADR-0017 Seal/Open/Rotate contract, and third-party adapters need not implement it): callers
// on the rotate/import path type-assert —
//
//	if fp, ok := backend.(secrets.Refingerprinter); ok { _ = fp.RefingerprintOnRotate(ctx, id, pt) }
//
// # Pepper rotation procedure (doc 05 §7.4)
//
// The fingerprint pepper (DASH_FINGERPRINT_PEPPER) keys only the provider_key duplicate-detection
// HMAC; it never participates in envelope confidentiality (that is the AES-256-GCM DEK/KEK path),
// so rotating it cannot lose or corrupt any secret. There is a single aad_fingerprint column and no
// pepper version, so a change is handled WITHOUT a bulk re-hash:
//
//  1. The operator sets a new DASH_FINGERPRINT_PEPPER; the backend is reconstructed with it, so
//     every new Seal (and every Rotate, which re-seals) fingerprints under the new pepper.
//  2. Envelopes not touched by a rotate keep their prior-pepper fingerprints. Duplicate detection
//     across the boundary degrades gracefully — a pre-rotation key and a post-rotation key are not
//     comparable (their HMACs use different peppers) — rather than breaking. Dedup is a soft
//     operator convenience, not a security control, so this is acceptable and expected.
//  3. As each key is next rotated or re-imported (plaintext legitimately in hand), the caller uses
//     RefingerprintOnRotate to recompute that one envelope's fingerprint under the new pepper,
//     migrating fingerprints opportunistically instead of decrypting the whole store at once.
type Refingerprinter interface {
	// RefingerprintOnRotate recomputes envelope id's provider_key fingerprint under the backend's
	// current pepper from plaintext (already in hand at a rotate/import) and stores it in place. It
	// is a no-op for non-provider_key kinds and returns ErrNotFound for an unknown id.
	RefingerprintOnRotate(ctx context.Context, id EnvelopeID, plaintext []byte) error
}

// encodeBytea renders a byte slice as Postgres bytea text input (\x hex); decodeBytea reverses
// the default hex output. The internal/pg client sends parameters in text format and has no
// []byte encoder, so bytea columns are round-tripped through these helpers.
func encodeBytea(b []byte) string { return `\x` + hex.EncodeToString(b) }

func decodeBytea(s string) ([]byte, error) {
	if len(s) >= 2 && s[0] == '\\' && (s[1] == 'x' || s[1] == 'X') {
		return hex.DecodeString(s[2:])
	}
	return nil, ErrCorruptEnvelope
}
