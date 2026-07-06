package secrets

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Keyring holds the master KEKs. Two (or more) can be live during a rotation window; the
// active entry seals new envelopes while any listed entry can unwrap an existing DEK
// (doc 05 §7.2).
type Keyring struct {
	keys   map[string][]byte // master_key_id -> 32-byte KEK
	active string            // id of the sealing KEK
}

// Keyring sentinel errors.
var (
	// ErrNoMasterKey reports an empty or all-invalid DASH_MASTER_KEY.
	ErrNoMasterKey = errors.New("secrets: no master key configured")
	// ErrKeySize reports a KEK that is not exactly 32 bytes (AES-256).
	ErrKeySize = errors.New("secrets: master key must be 32 bytes")
	// ErrUnknownKeyID reports an envelope referencing a master key absent from the keyring.
	ErrUnknownKeyID = errors.New("secrets: unknown master key id")
)

// keyID derives a stable 8-hex-char id from a KEK: the first 4 bytes of sha256(key). It never
// exposes the key itself.
func keyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:4])
}

// NewKeyring parses DASH_MASTER_KEY: a comma-separated list of base64-encoded 32-byte keys.
// Each entry's id is keyID(key); the FIRST entry is the active sealing KEK. It errors if no
// valid key is present or any entry is the wrong size.
//
// Note (deviation from doc 05 §7.2's illustrative "<id>:<base64>" form): the id is DERIVED
// from the key here rather than supplied inline, matching NewKeyringFromKeys and keeping the
// env format a bare key list. Ids remain stable and collision-resistant (sha256 prefix).
func NewKeyring(env string) (*Keyring, error) {
	keys := map[string][]byte{}
	active := ""
	for _, part := range strings.Split(env, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, err := decodeKey(part)
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("%w: got %d bytes", ErrKeySize, len(key))
		}
		id := keyID(key)
		if _, dup := keys[id]; dup {
			continue // identical key listed twice; keep the first
		}
		keys[id] = key
		if active == "" {
			active = id
		}
	}
	if active == "" {
		return nil, ErrNoMasterKey
	}
	return &Keyring{keys: keys, active: active}, nil
}

// NewKeyringFromKeys builds a keyring from explicit ids (for tests and the rotation runbook).
// active must name one of the keys, and every key must be 32 bytes.
func NewKeyringFromKeys(active string, keys map[string][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, ErrNoMasterKey
	}
	cp := make(map[string][]byte, len(keys))
	for id, k := range keys {
		if len(k) != 32 {
			return nil, fmt.Errorf("%w: key %q has %d bytes", ErrKeySize, id, len(k))
		}
		cp[id] = append([]byte(nil), k...)
	}
	if _, ok := cp[active]; !ok {
		return nil, fmt.Errorf("%w: active %q", ErrUnknownKeyID, active)
	}
	return &Keyring{keys: cp, active: active}, nil
}

// Active returns the id and bytes of the sealing KEK.
func (kr *Keyring) Active() (id string, kek []byte) {
	return kr.active, kr.keys[kr.active]
}

// Get returns the KEK for id and whether it is present.
func (kr *Keyring) Get(id string) ([]byte, bool) {
	k, ok := kr.keys[id]
	return k, ok
}

// decodeKey accepts standard or raw (unpadded) base64.
func decodeKey(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
