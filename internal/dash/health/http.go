package health

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// basePath is the mount point for the Provider Health Center (doc 04 §2.5).
const basePath = "/v1/admin/health"

const maxBodyBytes = 1 << 20 // 1 MiB

// Error codes (doc 04 §1.6 registry; identical envelope to internal/dash/httpx, whose writeError is
// unexported — this surface emits the same shape rather than importing it, matching providers/keys).
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeUnauthorized     = "unauthorized"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeIdempotencyReuse = "idempotency_key_reuse"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// handlers is the HTTP adapter around a Service.
type handlers struct {
	svc  *Service
	auth Authenticator
	idem *idemLedger
	log  *slog.Logger
}

// Routes registers the 6 Provider Health endpoints (doc 04 §2.5) on mux, each wrapped in the
// package's own authenticate -> RBAC (-> idempotency on writes) chain. Health data is Class P
// (platform_only), so every route is operator-only. CSRF / MFA / IP-allowlist come from the shared
// httpx FeatureChain when co-mounted; the orchestrator adds "health" to the feature-prefix list.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: NewService(d), auth: d.Auth, idem: newIdemLedger(), log: log}
	registerRoutes(mux, h)
}

// registerRoutes mounts the endpoint table (split out so tests wire a fake-backed handlers).
func registerRoutes(mux *http.ServeMux, h *handlers) {
	mux.HandleFunc("GET "+basePath+"/providers", h.readOp(h.listProviders))
	mux.HandleFunc("GET "+basePath+"/providers/{id}/timeline", h.readOp(h.timeline))
	mux.HandleFunc("GET "+basePath+"/schedules", h.readOp(h.listSchedules))
	mux.HandleFunc("PUT "+basePath+"/schedules/{id}", h.write(h.putSchedule))
	mux.HandleFunc("POST "+basePath+"/checks/run", h.write(h.runCheck))
	mux.HandleFunc("GET "+basePath+"/regional", h.readOp(h.regional))
}

// --- middleware chain ---

func (h *handlers) readOp(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(rbac.ProvidersRead, h.requireOperator(next)))
}

func (h *handlers) write(next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(rbac.ProvidersWrite, h.requireOperator(h.idempotency(next))))
}

// authenticate binds the verified Principal into ctx (G1); a no-op when an upstream chain already
// bound one, so this surface composes with or without an outer authenticator.
func (h *handlers) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, err := tenant.FromContext(ctx); err != nil {
			if h.auth == nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			p, err := h.auth.Authenticate(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			ctx = tenant.WithPrincipal(ctx, p)
		}
		next(w, r.WithContext(ctx))
	}
}

func (h *handlers) requireRole(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), action).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		next(w, r)
	}
}

// requireOperator confines the health surface to operators: Provider health is Class P platform
// data (doc 05 §2), never exposed to tenant roles.
func (h *handlers) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		if db.RoleFromPrincipal(p) != rbac.RoleOperator {
			writeError(w, http.StatusForbidden, codeForbidden, "operator role required")
			return
		}
		next(w, r)
	}
}

// --- read handlers ---

func (h *handlers) listProviders(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ProviderStatuses(r.Context())
	if err != nil {
		h.fail(w, "providers", err)
		return
	}
	if items == nil {
		items = []ProviderStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) timeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	gran := q.Get("granularity")
	if gran == "" {
		gran = q.Get("gran")
	}
	if gran == "" {
		gran = "day"
	}
	now := h.svc.now()
	var defFrom time.Time
	if gran == "hour" {
		defFrom = now.Add(-time.Duration(maxHourBuckets) * time.Hour)
	} else {
		defFrom = now.AddDate(0, 0, -maxDayBuckets)
	}
	from, ok := parseTime(w, q.Get("from"), defFrom)
	if !ok {
		return
	}
	to, ok := parseTime(w, q.Get("to"), now)
	if !ok {
		return
	}
	if !to.After(from) {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "to must be after from")
		return
	}
	if to.Sub(from) > time.Duration(maxWindowDays)*24*time.Hour {
		writeError(w, http.StatusBadRequest, codeValidationFailed, "window exceeds retention bound")
		return
	}
	res, err := h.svc.Timeline(r.Context(), id, from, to, gran)
	if err != nil {
		h.fail(w, "timeline", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *handlers) listSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListSchedules(r.Context())
	if err != nil {
		h.fail(w, "schedules", err)
		return
	}
	if items == nil {
		items = []Schedule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) regional(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := h.svc.now()
	from, ok := parseTime(w, q.Get("from"), now.Add(-24*time.Hour))
	if !ok {
		return
	}
	to, ok := parseTime(w, q.Get("to"), now)
	if !ok {
		return
	}
	if !to.After(from) {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "to must be after from")
		return
	}
	items, err := h.svc.Regional(r.Context(), from, to)
	if err != nil {
		h.fail(w, "regional", err)
		return
	}
	if items == nil {
		items = []RegionAgg{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- write handlers ---

type scheduleReq struct {
	IntervalS *int     `json:"interval_s"`
	JitterPct *int     `json:"jitter_pct"`
	Regions   []string `json:"regions"`
	Enabled   *bool    `json:"enabled"`
}

func (h *handlers) putSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleReq
	if !decodeJSON(w, r, &req) {
		return
	}
	// Defaults for omitted fields (doc 10 §3.3): 60s cadence, 10% jitter, enabled.
	s := Schedule{
		ProviderID: r.PathValue("id"),
		IntervalS:  60,
		JitterPct:  10,
		Regions:    req.Regions,
		Enabled:    true,
	}
	if req.IntervalS != nil {
		s.IntervalS = *req.IntervalS
	}
	if req.JitterPct != nil {
		s.JitterPct = *req.JitterPct
	}
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	out, err := h.svc.UpsertSchedule(r.Context(), s)
	if err != nil {
		h.fail(w, "put-schedule", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type runCheckReq struct {
	ProviderID string `json:"provider_id"`
}

func (h *handlers) runCheck(w http.ResponseWriter, r *http.Request) {
	var req runCheckReq
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := h.svc.RunCheck(r.Context(), req.ProviderID)
	if err != nil {
		h.fail(w, "run-check", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider_id": req.ProviderID, "result": res})
}

// fail maps a service error to a uniform status.
func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "provider not found")
	case errors.Is(err, ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, validationMsg(err))
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("health handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- request parsing helpers ---

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if len(bytes.TrimSpace(body)) == 0 {
		return true // empty body => all-default request
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

// parseTime parses an RFC3339 timestamp or a unix-seconds integer; empty => def.
func parseTime(w http.ResponseWriter, s string, def time.Time) (time.Time, bool) {
	if s == "" {
		return def, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), true
	}
	writeError(w, http.StatusBadRequest, codeValidationFailed, "from/to must be RFC3339 or unix seconds")
	return time.Time{}, false
}

// --- idempotency (doc 04 §1.3): in-process ledger equivalent to httpx's unexported middleware ---

type idemLedger struct {
	mu      sync.Mutex
	entries map[string]*idemEntry
}

type idemEntry struct {
	hash   [32]byte
	status int
	body   []byte
	done   bool
}

func newIdemLedger() *idemLedger { return &idemLedger{entries: map[string]*idemEntry{}} }

func (h *handlers) idempotency(next http.HandlerFunc) http.HandlerFunc {
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

		h.idem.mu.Lock()
		e, seen := h.idem.entries[lk]
		if seen {
			if subtle.ConstantTimeCompare(e.hash[:], sum[:]) != 1 {
				h.idem.mu.Unlock()
				writeError(w, http.StatusConflict, codeIdempotencyReuse, "Idempotency-Key was reused with a different request body")
				return
			}
			if e.done {
				status, respBody := e.status, e.body
				h.idem.mu.Unlock()
				w.Header().Set("Idempotency-Replayed", "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(respBody)
				return
			}
			h.idem.mu.Unlock()
			next(w, r)
			return
		}
		e = &idemEntry{hash: sum}
		h.idem.entries[lk] = e
		h.idem.mu.Unlock()

		cw := &captureWriter{header: w.Header().Clone(), status: http.StatusOK}
		next(cw, r)

		h.idem.mu.Lock()
		e.status, e.body, e.done = cw.status, cw.buf.Bytes(), true
		h.idem.mu.Unlock()

		for k, vs := range cw.header {
			for _, v := range vs {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(cw.status)
		_, _ = w.Write(cw.buf.Bytes())
	}
}

type captureWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(code int)        { c.status = code }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }

// --- response + misc helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

// validationError carries a human message alongside the ErrValidation sentinel.
type validationError struct{ msg string }

func (e validationError) Error() string { return "health: " + e.msg }
func (e validationError) Unwrap() error { return ErrValidation }

func wrapValidation(msg string) error { return validationError{msg: msg} }

func validationMsg(err error) string {
	var ve validationError
	if errors.As(err, &ve) {
		return ve.msg
	}
	return "validation failed"
}

func itoa(n int) string { return strconv.Itoa(n) }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
