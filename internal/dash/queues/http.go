package queues

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/metrics"
	"github.com/enrichment/waterfall/internal/tenant"
)

// HTTP surface for module 8 (doc 04 §2.8): the 8 Queue / dead-letter / replay endpoints under
// /v1/admin. Routes mounts fully-wrapped handlers so an orchestrator only calls Routes(mux, d):
// authentication (injected Authenticator), RBAC (rbac.Can), and Idempotency-Key enforcement on
// writes are applied here; audit + RLS are enforced in the service/persistence layers. This
// package never imports internal/dash/httpx (coordinate avoidance).

const (
	basePath     = "/v1/admin"
	maxBodyBytes = 1 << 20
)

// Error codes (doc 04 §1.6 registry subset).
const (
	codeInvalidJSON    = "invalid_json"
	codeMissingIdemKey = "missing_idempotency_key"
	codeInvalidCursor  = "invalid_cursor"
	codeInvalidFilter  = "invalid_filter"
	codeWindowRange    = "window_out_of_range"
	codeUnauthorized   = "unauthorized"
	codeForbidden      = "forbidden"
	codeNotFound       = "not_found"
	codeBulkConflict   = "bulk_job_conflict"
	codeJobTerminal    = "job_terminal"
	codeInternal       = "internal"
)

// Authenticator binds the verified Principal for a request (satisfied by httpx.CtxAuthenticator).
type Authenticator interface {
	Authenticate(*http.Request) (tenant.Principal, error)
}

// Deps are the constructed dependencies Routes needs.
type Deps struct {
	Store            *db.Store
	Outbox           OutboxRedriver // pgoutbox.Store — the one job_outbox write path (redrive)
	Audit            *audit.Log
	Auth             Authenticator
	Metrics          *metrics.Registry
	Logger           *slog.Logger
	InstanceID       string
	ReplayRatePerMin int
}

type router struct {
	svc  *Service
	auth Authenticator
	log  *slog.Logger
}

// Routes constructs the Service and mounts the 8 §2.8 endpoints on mux.
func Routes(mux *http.ServeMux, d Deps) *Service {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	svc := NewService(Config{
		Store: d.Store, Outbox: d.Outbox, Audit: d.Audit, Metrics: d.Metrics,
		Logger: logger, InstanceID: d.InstanceID, ReplayRatePerMin: d.ReplayRatePerMin,
	})
	rt := &router{svc: svc, auth: d.Auth, log: logger}
	rt.register(mux)
	return svc
}

func (rt *router) register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+basePath+"/queues", rt.read(rbac.QueuesRead, rt.listQueues))
	mux.HandleFunc("GET "+basePath+"/queues/{name}/stats", rt.read(rbac.QueuesRead, rt.queueStats))
	mux.HandleFunc("GET "+basePath+"/queues/{name}/jobs", rt.read(rbac.QueuesRead, rt.listJobs))
	mux.HandleFunc("GET "+basePath+"/dead-letters", rt.read(rbac.QueuesRead, rt.listDeadLetters))
	mux.HandleFunc("POST "+basePath+"/dead-letters/{id}/redrive", rt.write(rbac.QueuesReplay, rt.redrive))
	mux.HandleFunc("POST "+basePath+"/queues/{name}/replay", rt.write(rbac.QueuesReplay, rt.replay))
	mux.HandleFunc("GET "+basePath+"/jobs/{id}", rt.read(rbac.QueuesRead, rt.jobDetail))
	mux.HandleFunc("PUT "+basePath+"/queues/{name}/workers", rt.write(rbac.WorkersActions, rt.scale))
}

// BulkJobsRoute mounts GET /v1/admin/bulk-jobs/{id} backed by the DURABLE bulk_jobs table
// (replay progress). It is separate from Routes so the orchestrator can wire the single durable
// owner of this shared endpoint (superseding the keys package's P1 in-process registry —
// OI-KEYS-1). Mount exactly one owner to avoid a duplicate-pattern panic.
func BulkJobsRoute(mux *http.ServeMux, d Deps, svc *Service) {
	rt := &router{svc: svc, auth: d.Auth, log: d.Logger}
	if rt.log == nil {
		rt.log = slog.Default()
	}
	mux.HandleFunc("GET "+basePath+"/bulk-jobs/{id}", rt.read(rbac.QueuesRead, rt.bulkStatus))
}

// --- middleware ---

func (rt *router) read(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(a, h))
}

func (rt *router) write(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(a, rt.requireIdem(h)))
}

func (rt *router) authenticate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := rt.auth.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
			return
		}
		h(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}
}

func (rt *router) requireRole(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), a).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		h(w, r)
	}
}

func (rt *router) requireIdem(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Idempotency-Key"); k == "" || len(k) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		h(w, r)
	}
}

// --- handlers ---

func (rt *router) listQueues(w http.ResponseWriter, r *http.Request) {
	items, err := rt.svc.Queues(r.Context())
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	dtos := make([]queueDTO, 0, len(items))
	for _, q := range items {
		dtos = append(dtos, toQueueDTO(q))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}

func (rt *router) queueStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := q.Get("res")
	if res == "" {
		res = "1m"
	}
	if res != "1m" && res != "1h" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "res must be 1m or 1h")
		return
	}
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "from must be RFC3339")
			return
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "to must be RFC3339")
			return
		}
		to = t
	}
	buckets, err := rt.svc.Stats(r.Context(), r.PathValue("name"), Window{Res: res, From: from, To: to})
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	out := make([]bucketDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, toBucketDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": r.PathValue("name"), "res": res, "buckets": out})
}

func (rt *router) listJobs(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	state := State(r.URL.Query().Get("state"))
	if state == "" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "the state filter is required")
		return
	}
	if !ValidState(state) {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "unknown state")
		return
	}
	items, next, err := rt.svc.Jobs(r.Context(), r.PathValue("name"), state, cur, limit)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	dtos := make([]jobDTO, 0, len(items))
	for _, j := range items {
		dtos = append(dtos, toJobDTO(j))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: dtos, NextCursor: cursorOut(encodeCursor(next))})
}

func (rt *router) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	f := deadFilterFrom(r)
	items, next, err := rt.svc.DeadLetters(r.Context(), f, cur, limit)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	dtos := make([]deadDTO, 0, len(items))
	for _, d := range items {
		dtos = append(dtos, toDeadDTO(d))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: dtos, NextCursor: cursorOut(encodeCursor(next))})
}

func (rt *router) redrive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ok, err := rt.svc.Redrive(r.Context(), id)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "no such dead-lettered job")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job_id": id, "redriven": true})
}

func (rt *router) replay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filter struct {
			ErrorClass json.RawMessage `json:"error_class"`
			Before     string          `json:"before"`
			After      string          `json:"after"`
		} `json:"filter"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	f := DeadFilter{ErrorClass: firstErrorClass(req.Filter.ErrorClass)}
	if req.Filter.Before != "" {
		if t, err := time.Parse(time.RFC3339, req.Filter.Before); err == nil {
			f.Before = t
		}
	}
	if req.Filter.After != "" {
		if t, err := time.Parse(time.RFC3339, req.Filter.After); err == nil {
			f.After = t
		}
	}
	jobID, err := rt.svc.Replay(r.Context(), r.PathValue("name"), f)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (rt *router) jobDetail(w http.ResponseWriter, r *http.Request) {
	d, err := rt.svc.JobDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobDetailDTO(d))
}

func (rt *router) scale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replicas        *int `json:"replicas"`
		DesiredReplicas *int `json:"desired_replicas"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	n := req.Replicas
	if n == nil {
		n = req.DesiredReplicas
	}
	if n == nil {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "replicas is required")
		return
	}
	if err := rt.svc.SetScaleIntent(r.Context(), r.PathValue("name"), *n); err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue": r.PathValue("name"), "desired_replicas": *n,
		"note": "intent only — actuation is deploy-layer (HPA/ASG/deploy tool); the dashboard cannot spawn processes",
	})
}

func (rt *router) bulkStatus(w http.ResponseWriter, r *http.Request) {
	j, ok, err := rt.svc.BulkJobStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "bulk job not found")
		return
	}
	writeJSON(w, http.StatusOK, toBulkJobDTO(j))
}

// --- error mapping ---

func (rt *router) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "resource not found")
	case errors.Is(err, ErrInvalidFilter):
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "invalid filter")
	case errors.Is(err, ErrWindowOutOfRange):
		writeError(w, http.StatusBadRequest, codeWindowRange, "requested window is out of range")
	case errors.Is(err, ErrReplayInFlight):
		writeError(w, http.StatusConflict, codeBulkConflict, "a replay is already in flight for this queue")
	case errors.Is(err, ErrJobTerminal):
		writeError(w, http.StatusConflict, codeJobTerminal, "bulk job is already in a terminal state")
	default:
		rt.log.Error("queues handler error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// deadFilterFrom parses the ?error_class=&before=&after= dead-letter filters.
func deadFilterFrom(r *http.Request) DeadFilter {
	q := r.URL.Query()
	f := DeadFilter{ErrorClass: q.Get("error_class")}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Before = t
		}
	}
	if v := q.Get("after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.After = t
		}
	}
	return f
}

// firstErrorClass accepts either a JSON string or array (doc 04 §2.8 shows an array) and returns
// the first class token (pgoutbox matches it against last_error).
func firstErrorClass(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr[0]
	}
	return ""
}

// --- shared HTTP helpers (kept local; no httpx dependency) ---

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
	var eb errorBody
	eb.Error.Code = code
	eb.Error.Message = msg
	writeJSON(w, status, eb)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

func parseCursor(w http.ResponseWriter, r *http.Request) (db.Cursor, bool) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return db.Cursor{}, false
	}
	return cur, true
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be between 1 and 200")
		return 0, false
	}
	return n, true
}

func encodeCursor(c db.Cursor) string {
	if len(c.K) == 0 && c.ID == "" {
		return ""
	}
	return db.EncodeCursor(c)
}

func cursorOut(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// listEnvelope is the uniform paginated list response (doc 04 §1.4).
type listEnvelope struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
