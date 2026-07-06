package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// idemStore is the admin idempotency ledger seam keyed (tenant, key) (doc 04 §1.3). It has two
// implementations: newDurableLedger's Postgres backend over dash_admin_idempotency (production,
// first-writer-wins, survives restarts — OI-API-8, resolving Deviation D-P0-2), and the in-memory
// map below (unit tests). The middleware contract is identical across both: a repeat with an
// identical body replays the stored response with Idempotency-Replayed: true; a repeat with a
// DIFFERENT body is 409 idempotency_key_reuse; concurrent same-key requests execute the handler
// exactly once (the first writer runs it, contenders replay its stored response).
type idemStore interface {
	// Claim attempts to become the first writer for (tenant, key) with bodyHash. claimed=true
	// means this caller inserted the row and MUST run the handler then call Finish; claimed=false
	// means the key is already held — the caller resolves via Lookup.
	Claim(ctx context.Context, tenantID, key string, bodyHash []byte) (claimed bool, err error)
	// Lookup returns the current record for (tenant, key); found=false when absent.
	Lookup(ctx context.Context, tenantID, key string) (rec idemRecord, found bool, err error)
	// Finish records the terminal status+response for the claimed (tenant, key).
	Finish(ctx context.Context, tenantID, key string, status int, response []byte) error
	// DeleteBefore reaps ledger rows created before cutoff (retention hook).
	DeleteBefore(ctx context.Context, cutoff time.Time) error
}

// idemRecord is the persisted state of one idempotency key.
type idemRecord struct {
	bodyHash []byte
	done     bool   // the first writer has recorded a terminal response
	status   int    // valid when done
	response []byte // stored response body, valid when done
}

// idemLedger is the Server's idempotency ledger. It delegates to an idemStore backend: the durable
// Postgres ledger in production (newDurableLedger), an in-memory map in unit tests (newIdemLedger).
// The type name and the no-arg newIdemLedger constructor are preserved so the Server wiring stays
// source-compatible; NewServer swaps to newDurableLedger(Deps.Store) to enable durability.
type idemLedger struct{ backend idemStore }

// newIdemLedger builds an in-memory ledger (unit tests and the default wiring).
func newIdemLedger() *idemLedger { return &idemLedger{backend: newMemIdemStore()} }

// newDurableLedger builds the production ledger over dash_admin_idempotency (OI-API-8). NewServer
// wires this with Deps.Store so admin idempotency survives process restarts and is shared across
// replicas.
func newDurableLedger(store *db.Store) *idemLedger {
	return &idemLedger{backend: newPGIdemStore(store)}
}

// durableOrMemLedger picks the durable Postgres ledger when a Store is wired (production /
// integration), else the in-memory ledger (unit tests that construct a Server without a Store).
func durableOrMemLedger(store *db.Store) *idemLedger {
	if store == nil {
		return newIdemLedger()
	}
	return newDurableLedger(store)
}

func (l *idemLedger) Claim(ctx context.Context, tenantID, key string, bodyHash []byte) (bool, error) {
	return l.backend.Claim(ctx, tenantID, key, bodyHash)
}
func (l *idemLedger) Lookup(ctx context.Context, tenantID, key string) (idemRecord, bool, error) {
	return l.backend.Lookup(ctx, tenantID, key)
}
func (l *idemLedger) Finish(ctx context.Context, tenantID, key string, status int, response []byte) error {
	return l.backend.Finish(ctx, tenantID, key, status, response)
}
func (l *idemLedger) DeleteBefore(ctx context.Context, cutoff time.Time) error {
	return l.backend.DeleteBefore(ctx, cutoff)
}

// ReapIdempotency deletes admin idempotency ledger rows created before cutoff (doc 04 §1.3: 24h
// retention). Intended to run on the shared reaper loop alongside Sessions.DeleteExpired and the
// mfa_used_steps reaper; exported so the orchestrator can wire it there.
func (s *Server) ReapIdempotency(ctx context.Context, cutoff time.Time) error {
	return s.idem.DeleteBefore(ctx, cutoff)
}

// memIdemStore is the in-memory idemStore for unit tests. It mirrors the durable ledger's semantics
// exactly (first-writer-wins, same-body replay, different-body conflict) but holds records in a map.
type memIdemStore struct {
	mu      sync.Mutex
	entries map[string]*memIdemEntry
}

type memIdemEntry struct {
	rec       idemRecord
	createdAt time.Time
}

func newMemIdemStore() *memIdemStore { return &memIdemStore{entries: map[string]*memIdemEntry{}} }

func memIdemKey(tenantID, key string) string { return tenantID + "\x00" + key }

func (m *memIdemStore) Claim(_ context.Context, tenantID, key string, bodyHash []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memIdemKey(tenantID, key)
	if _, ok := m.entries[k]; ok {
		return false, nil
	}
	m.entries[k] = &memIdemEntry{
		rec:       idemRecord{bodyHash: append([]byte(nil), bodyHash...)},
		createdAt: time.Now(),
	}
	return true, nil
}

func (m *memIdemStore) Lookup(_ context.Context, tenantID, key string) (idemRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[memIdemKey(tenantID, key)]
	if !ok {
		return idemRecord{}, false, nil
	}
	return e.rec, true, nil
}

func (m *memIdemStore) Finish(_ context.Context, tenantID, key string, status int, response []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[memIdemKey(tenantID, key)]
	if !ok {
		return nil
	}
	e.rec.done = true
	e.rec.status = status
	e.rec.response = append([]byte(nil), response...)
	return nil
}

func (m *memIdemStore) DeleteBefore(_ context.Context, cutoff time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.entries {
		if e.createdAt.Before(cutoff) {
			delete(m.entries, k)
		}
	}
	return nil
}

// captureWriter buffers the handler's response so it can be recorded for replay and then flushed.
type captureWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(code int)        { c.status = code }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }

// idemContendPoll bounds how long a contender waits for the first writer to finish before giving up
// (in-flight duplicate). 100 × 20ms = 2s: comfortably longer than any admin write, short enough to
// never wedge a request. On timeout the contender returns 409 conflict rather than double-execute.
const (
	idemContendAttempts = 100
	idemContendInterval = 20 * time.Millisecond
)

// idempotency enforces the Idempotency-Key header on writes and the reuse rules. It buffers the
// body (so the handler can re-read it) and hashes it to detect same-key/different-body reuse, then
// claims the key in the durable ledger: the first writer runs the handler and records the response;
// any contender with the same body replays it, with a different body gets 409.
func (s *Server) idempotency(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		r.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(body)

		p, _ := tenant.FromContext(r.Context())
		tid := p.TenantID

		claimed, err := s.idem.Claim(r.Context(), tid, key, sum[:])
		if err != nil {
			s.log().Error("idempotency claim failed", "err", err)
			writeError(w, http.StatusInternalServerError, codeInternal, "idempotency ledger unavailable")
			return
		}
		if claimed {
			s.runAndRecord(w, r, tid, key, h)
			return
		}
		s.replayOrConflict(w, r, tid, key, sum[:], h)
	}
}

// runAndRecord executes the handler as the first writer, records its terminal response in the
// ledger, then flushes the buffered response to the client.
func (s *Server) runAndRecord(w http.ResponseWriter, r *http.Request, tid, key string, h http.HandlerFunc) {
	cw := &captureWriter{header: w.Header().Clone(), status: http.StatusOK}
	h(cw, r)
	if err := s.idem.Finish(r.Context(), tid, key, cw.status, cw.buf.Bytes()); err != nil {
		// The response is valid regardless; a failed Finish only forfeits replay of this key.
		s.log().Error("idempotency finish failed", "err", err)
	}
	for k, vs := range cw.header {
		for _, v := range vs {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(cw.status)
	_, _ = w.Write(cw.buf.Bytes())
}

// replayOrConflict resolves a request whose key is already held: same body + completed => replay
// the stored response; different body => 409 idempotency_key_reuse; same body still in flight =>
// wait (bounded) for the first writer, then replay, or 409 conflict on timeout. It never executes
// the handler, so a claimed key runs exactly once.
func (s *Server) replayOrConflict(w http.ResponseWriter, r *http.Request, tid, key string, sum []byte, h http.HandlerFunc) {
	for attempt := 0; attempt < idemContendAttempts; attempt++ {
		rec, found, err := s.idem.Lookup(r.Context(), tid, key)
		if err != nil {
			s.log().Error("idempotency lookup failed", "err", err)
			writeError(w, http.StatusInternalServerError, codeInternal, "idempotency ledger unavailable")
			return
		}
		if !found {
			// The first writer's claim vanished (rolled back). Try to become the writer ourselves.
			claimed, cerr := s.idem.Claim(r.Context(), tid, key, sum)
			if cerr != nil {
				writeError(w, http.StatusInternalServerError, codeInternal, "idempotency ledger unavailable")
				return
			}
			if claimed {
				s.runAndRecord(w, r, tid, key, h)
				return
			}
			continue
		}
		if subtle.ConstantTimeCompare(rec.bodyHash, sum) != 1 {
			writeError(w, http.StatusConflict, codeIdempotencyReuse,
				"Idempotency-Key was reused with a different request body")
			return
		}
		if rec.done {
			w.Header().Set("Idempotency-Replayed", "true")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.response)
			return
		}
		// Same body, first writer still in flight: wait and re-check rather than double-execute.
		time.Sleep(idemContendInterval)
	}
	writeError(w, http.StatusConflict, codeConflict,
		"a request with this Idempotency-Key is still in progress")
}
