package httpx

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"net/http"
	"sync"

	"github.com/enrichment/waterfall/internal/tenant"
)

// idemLedger is the dash-scoped admin idempotency ledger keyed (tenant, key) (doc 04 §1.3). It is
// in-process for P0 (Deviation D-P0-2: a durable table replaces it in a later phase): a repeat with
// an identical body replays the stored response; a repeat with a DIFFERENT body is 409.
type idemLedger struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
}

type idemEntry struct {
	hash   []byte
	status int
	body   []byte
	done   bool
}

func newIdemLedger() *idemLedger { return &idemLedger{entries: map[string]*idemEntry{}} }

// captureWriter buffers the handler's response so it can be recorded for replay and then flushed.
type captureWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(code int)        { c.status = code }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }

// idempotency enforces the Idempotency-Key header on writes and the reuse rules. It buffers the
// body (so the handler can re-read it) and hashes it to detect same-key/different-body reuse.
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
		lk := p.TenantID + "\x00" + key

		s.idem.mu.Lock()
		e, seen := s.idem.entries[lk]
		if seen {
			if subtle.ConstantTimeCompare(e.hash, sum[:]) != 1 {
				s.idem.mu.Unlock()
				writeError(w, http.StatusConflict, codeIdempotencyReuse,
					"Idempotency-Key was reused with a different request body")
				return
			}
			if e.done {
				status, respBody := e.status, e.body
				s.idem.mu.Unlock()
				w.Header().Set("Idempotency-Replayed", "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(respBody)
				return
			}
			// In-flight duplicate (rare): let it proceed rather than block.
			s.idem.mu.Unlock()
			h(w, r)
			return
		}
		e = &idemEntry{hash: sum[:]}
		s.idem.entries[lk] = e
		s.idem.mu.Unlock()

		cw := &captureWriter{header: w.Header().Clone(), status: http.StatusOK}
		h(cw, r)

		s.idem.mu.Lock()
		e.status = cw.status
		e.body = cw.buf.Bytes()
		e.done = true
		s.idem.mu.Unlock()

		for k, vs := range cw.header {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(cw.status)
		_, _ = w.Write(cw.buf.Bytes())
	}
}
