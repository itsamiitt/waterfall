package workers

import (
	"context"
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
	"github.com/enrichment/waterfall/internal/tenant"
)

// HTTP surface for module 9 (doc 04 §2.9): fleet list/detail, the 10s heartbeat channel, the
// desired-state actions, scale intent, rolling restart, and worker stats. Routes mounts
// fully-wrapped handlers (authenticate -> RBAC -> Idempotency-Key on writes); audit + RLS live
// in the service/store. No httpx dependency (coordinate avoidance).

const (
	basePath     = "/v1/admin"
	maxBodyBytes = 1 << 20
)

const (
	codeInvalidJSON    = "invalid_json"
	codeMissingIdemKey = "missing_idempotency_key"
	codeInvalidCursor  = "invalid_cursor"
	codeInvalidFilter  = "invalid_filter"
	codeUnauthorized   = "unauthorized"
	codeForbidden      = "forbidden"
	codeNotFound       = "not_found"
	codeConflict       = "bulk_job_conflict"
	codeValidation     = "validation_failed"
	codeInternal       = "internal"
)

// Authenticator binds the verified Principal (satisfied by httpx.CtxAuthenticator).
type Authenticator interface {
	Authenticate(*http.Request) (tenant.Principal, error)
}

// Deps are the constructed dependencies Routes needs.
type Deps struct {
	Store      *db.Store
	Audit      *audit.Log
	Auth       Authenticator
	Scaler     ScaleIntentSetter // queues.Service — the queue_defs.desired_replicas single writer
	Now        func() time.Time
	Logger     *slog.Logger
	InstanceID string
}

type router struct {
	svc  *Service
	auth Authenticator
	log  *slog.Logger
}

// Routes constructs the Service and mounts the worker endpoints on mux. Returns the Service so an
// orchestrator can also start the worker-lost detector loop over the same store.
func Routes(mux *http.ServeMux, d Deps) *Service {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	svc := NewService(ServiceConfig{
		Store: d.Store, Audit: d.Audit, Scaler: d.Scaler, Now: d.Now,
		Logger: logger, InstanceID: d.InstanceID,
	})
	rt := &router{svc: svc, auth: d.Auth, log: logger}
	rt.register(mux)
	return svc
}

func (rt *router) register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+basePath+"/workers", rt.read(rbac.WorkersRead, rt.listWorkers))
	mux.HandleFunc("GET "+basePath+"/workers/{id}", rt.read(rbac.WorkersRead, rt.getWorker))
	mux.HandleFunc("GET "+basePath+"/workers/{id}/stats", rt.read(rbac.WorkersRead, rt.workerStats))
	// Heartbeat is the worker control channel: authenticated + RBAC, but NOT Idempotency-gated
	// (a 10s beat is an idempotent upsert by worker id).
	mux.HandleFunc("POST "+basePath+"/workers/{id}/heartbeat", rt.beat(rbac.WorkersActions, rt.heartbeat))
	mux.HandleFunc("POST "+basePath+"/workers/{id}/restart", rt.write(rbac.WorkersActions, rt.restart))
	mux.HandleFunc("POST "+basePath+"/workers/{id}/drain", rt.write(rbac.WorkersActions, rt.drain))
	mux.HandleFunc("POST "+basePath+"/workers/{id}/pause", rt.write(rbac.WorkersActions, rt.pause))
	mux.HandleFunc("POST "+basePath+"/workers/{id}/resume", rt.write(rbac.WorkersActions, rt.resume))
	mux.HandleFunc("POST "+basePath+"/workers/scale", rt.write(rbac.WorkersActions, rt.scale))
	mux.HandleFunc("POST "+basePath+"/workers/rolling-restart", rt.write(rbac.WorkersActions, rt.rollingRestart))
}

// --- middleware ---

func (rt *router) read(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(a, h))
}
func (rt *router) write(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(a, rt.requireIdem(h)))
}
func (rt *router) beat(a rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(a, h))
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

func (rt *router) listWorkers(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := WorkerFilter{Kind: q.Get("kind"), Queue: q.Get("queue"), Region: q.Get("region"), Status: q.Get("status")}
	items, next, err := rt.svc.List(r.Context(), f, cur, limit)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	now := rt.svc.now()
	dtos := make([]workerDTO, 0, len(items))
	for _, wk := range items {
		dtos = append(dtos, toWorkerDTO(wk, now))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: dtos, NextCursor: cursorOut(encodeCursor(next))})
}

func (rt *router) getWorker(w http.ResponseWriter, r *http.Request) {
	wk, ok, err := rt.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "worker not found")
		return
	}
	writeJSON(w, http.StatusOK, toWorkerDTO(wk, rt.svc.now()))
}

func (rt *router) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req beatReq
	if !decodeJSON(w, r, &req) {
		return
	}
	b := Beat{
		ID: r.PathValue("id"), Kind: req.Kind, Region: req.Region, Queue: req.Queue,
		Version: req.Version, Status: req.Status, CPUPct: req.CPUPct, MemMB: req.MemMB,
		JobsActive: req.JobsActive, JobsDone: req.JobsDone,
	}
	wk, err := rt.svc.Heartbeat(r.Context(), b)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	// The response ECHOES desired_state — the worker's only control signal (doc 06 §4).
	writeJSON(w, http.StatusOK, toWorkerDTO(wk, rt.svc.now()))
}

func (rt *router) restart(w http.ResponseWriter, r *http.Request) { rt.action(w, r, rt.svc.Restart) }
func (rt *router) drain(w http.ResponseWriter, r *http.Request)   { rt.action(w, r, rt.svc.Drain) }
func (rt *router) pause(w http.ResponseWriter, r *http.Request)   { rt.action(w, r, rt.svc.Pause) }
func (rt *router) resume(w http.ResponseWriter, r *http.Request)  { rt.action(w, r, rt.svc.Resume) }

func (rt *router) action(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) (WorkerRow, error)) {
	wk, err := fn(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWorkerDTO(wk, rt.svc.now()))
}

func (rt *router) scale(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		Queue    string `json:"queue"`
		Replicas *int   `json:"replicas"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Queue == "" || req.Replicas == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "queue and replicas are required")
		return
	}
	if err := rt.svc.Scale(r.Context(), req.Queue, *req.Replicas); err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind": req.Kind, "queue": req.Queue, "replicas": *req.Replicas,
		"note": "intent only — actuation is deploy tooling (HPA/ASG); the dashboard cannot spawn or kill processes",
	})
}

func (rt *router) rollingRestart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind           string `json:"kind"`
		Queue          string `json:"queue"`
		MaxUnavailable int    `json:"max_unavailable"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	jobID, err := rt.svc.RollingRestart(r.Context(), req.Kind, req.Queue, req.MaxUnavailable)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (rt *router) workerStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().UTC()
	from, to := now.Add(-6*time.Hour), now
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	buckets, err := rt.svc.Stats(r.Context(), r.PathValue("id"), from, to)
	if err != nil {
		rt.writeErr(w, err)
		return
	}
	out := make([]statDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, toStatDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"worker_id": r.PathValue("id"), "buckets": out})
}

func (rt *router) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "worker not found")
	case errors.Is(err, ErrInFlight):
		writeError(w, http.StatusConflict, codeConflict, "a rolling restart is already in flight for this scope")
	default:
		rt.log.Error("workers handler error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// --- request/response DTOs ---

type beatReq struct {
	Kind       string  `json:"kind"`
	Region     string  `json:"region"`
	Queue      string  `json:"queue"`
	Version    string  `json:"version"`
	Status     string  `json:"status"`
	CPUPct     float64 `json:"cpu_pct"`
	MemMB      float64 `json:"mem_mb"`
	JobsActive int     `json:"jobs_active"`
	JobsDone   int64   `json:"jobs_done"`
}

type workerDTO struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind,omitempty"`
	Region          string  `json:"region,omitempty"`
	Queue           string  `json:"queue,omitempty"`
	Version         string  `json:"version,omitempty"`
	Status          string  `json:"status"`
	DesiredState    string  `json:"desired_state"`
	Converging      bool    `json:"converging"`
	HeartbeatAgeS   *int64  `json:"heartbeat_age_s"`
	JobsActive      int     `json:"jobs_active"`
	JobsDone        int64   `json:"jobs_done"`
	CPUPct          float64 `json:"cpu_pct"`
	MemMB           float64 `json:"mem_mb"`
	Restarts        int     `json:"restarts"`
	StartedAt       any     `json:"started_at"`
	LastHeartbeatAt any     `json:"last_heartbeat_at"`
}

func toWorkerDTO(w WorkerRow, now time.Time) workerDTO {
	d := workerDTO{
		ID: w.ID, Kind: w.Kind, Region: w.Region, Queue: w.Queue, Version: w.Version,
		Status: w.Status, DesiredState: w.DesiredState, Converging: !converged(w),
		JobsActive: w.JobsActive, JobsDone: w.JobsDone, CPUPct: w.CPUPct, MemMB: w.MemMB,
		Restarts: w.Restarts, StartedAt: nil, LastHeartbeatAt: nil,
	}
	if w.StartedAt != nil {
		d.StartedAt = w.StartedAt.UTC().Format(time.RFC3339)
	}
	if w.LastHeartbeatAt != nil {
		d.LastHeartbeatAt = w.LastHeartbeatAt.UTC().Format(time.RFC3339)
		age := int64(now.Sub(*w.LastHeartbeatAt).Seconds())
		if age < 0 {
			age = 0
		}
		d.HeartbeatAgeS = &age
	}
	return d
}

// converged reports whether a worker's reported status has reached its written intent (doc 06 §5).
func converged(w WorkerRow) bool {
	switch w.DesiredState {
	case DesiredRunning:
		return w.Status == StatusRunning
	case DesiredPaused:
		return w.Status == StatusPaused
	case DesiredDraining:
		return w.Status == StatusStopped && w.JobsActive == 0
	case DesiredStopped:
		return w.Status == StatusStopped
	}
	return false
}

type statDTO struct {
	BucketStart   string  `json:"bucket_start"`
	Beats         int     `json:"beats"`
	CPUPctAvg     float64 `json:"cpu_pct_avg"`
	MemMBAvg      float64 `json:"mem_mb_avg"`
	JobsActiveMax int     `json:"jobs_active_max"`
	JobsDoneDelta int64   `json:"jobs_done_delta"`
}

func toStatDTO(b WorkerStat) statDTO {
	return statDTO{
		BucketStart: b.BucketStart.UTC().Format(time.RFC3339), Beats: b.Beats,
		CPUPctAvg: b.CPUPctAvg, MemMBAvg: b.MemMBAvg, JobsActiveMax: b.JobsActiveMax,
		JobsDoneDelta: b.JobsDoneDelta,
	}
}

// --- shared HTTP helpers (kept local; no httpx dependency) ---

type listEnvelope struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

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
