package secrets

import (
	"context"
	"crypto/rand"
	"sync"
)

// MemoryBackend is an in-process Backend backed by a map, using the SAME real AES-256-GCM
// envelope crypto as PGBackend. It exists for unit tests that must not touch Postgres; it
// mints its own random KEK and fingerprint pepper on construction.
type MemoryBackend struct {
	kr     *Keyring
	pepper []byte

	mu   sync.Mutex
	rows map[EnvelopeID]envelope
}

// NewMemoryBackend builds a backend with a fresh random keyring and pepper.
func NewMemoryBackend() *MemoryBackend {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	pepper := make([]byte, 32)
	_, _ = rand.Read(pepper)
	id := keyID(key)
	kr, _ := NewKeyringFromKeys(id, map[string][]byte{id: key}) // cannot fail: 32-byte key, active present
	return &MemoryBackend{kr: kr, pepper: pepper, rows: map[EnvelopeID]envelope{}}
}

// Seal implements Backend.
func (m *MemoryBackend) Seal(_ context.Context, kind string, plaintext []byte) (EnvelopeID, error) {
	e, err := sealEnvelope(m.kr, m.pepper, kind, plaintext, "")
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.rows[e.id] = e
	m.mu.Unlock()
	return e.id, nil
}

// Open implements Backend.
func (m *MemoryBackend) Open(_ context.Context, id EnvelopeID) ([]byte, error) {
	m.mu.Lock()
	e, ok := m.rows[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	return openEnvelope(m.kr, e)
}

// Rotate implements Backend.
func (m *MemoryBackend) Rotate(_ context.Context, id EnvelopeID) (EnvelopeID, error) {
	m.mu.Lock()
	old, ok := m.rows[id]
	m.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	pt, err := openEnvelope(m.kr, old)
	if err != nil {
		return "", err
	}
	e, err := sealEnvelope(m.kr, m.pepper, old.kind, pt, string(id))
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.rows[e.id] = e
	m.mu.Unlock()
	return e.id, nil
}

// RefingerprintOnRotate recomputes envelope id's provider_key fingerprint under the backend's
// current pepper (from plaintext in hand at a rotate/import) and stores it in place — the in-memory
// mirror of PGBackend.RefingerprintOnRotate (OI-SEC-4). No-op for non-provider_key kinds.
func (m *MemoryBackend) RefingerprintOnRotate(_ context.Context, id EnvelopeID, plaintext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.rows[id]
	if !ok {
		return ErrNotFound
	}
	if e.kind != "provider_key" {
		return nil
	}
	e.fingerprint = fingerprintOf(m.pepper, plaintext)
	m.rows[id] = e
	return nil
}

var (
	_ Backend         = (*MemoryBackend)(nil)
	_ Refingerprinter = (*MemoryBackend)(nil)
	_ Refingerprinter = (*PGBackend)(nil)
)
