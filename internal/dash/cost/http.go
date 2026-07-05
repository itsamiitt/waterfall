package cost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

// basePaths (doc 04 §2.10). Cost analytics live under /v1/admin/cost; budgets under
// /v1/admin/budgets.
const (
	costBase     = "/v1/admin/cost"
	budgetsBase  = "/v1/admin/budgets"
	maxBodyBytes = 1 << 20
)

// Error codes (doc 04 §1.6 registry) — the subset this surface emits; shape matches the shared
// httpx envelope (writeError is unexported there, so this package emits the same shape to avoid a
// cycle).
const (
	codeInvalidJSON      = "invalid_json"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeUnauthorized     = "unauthorized"
	codeForbidden        = "forbidden"
	codeValidationFailed = "validation_failed"
	codeWindowOutOfRange = "window_out_of_range"
	codeInternal         = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// httpx.CtxAuthenticator). This package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// auditor is the consumer-side slice of audit.Log used to record operator cross-Tenant reads
// (ADR-0020: every handler serving rows under an operator cross-tenant policy audits). Satisfied
// by *audit.Log.
type auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

var _ auditor = (*audit.Log)(nil)

// Deps bundles the cost surface's collaborators. Service is the query engine; Auth resolves the
// Principal behind the shared FeatureChain; Audit records operator cross-Tenant reads.
type Deps struct {
	Service *Service
	Auth    Authenticator
	Audit   auditor
	Logger  *slog.Logger
}

type handlers struct {
	svc   *Service
	auth  Authenticator
	audit auditor
	log   *slog.Logger
}

// Routes mounts the 7 cost + budgets endpoints (doc 04 §2.10). CSRF / MFA / IP-allowlist come from
// the shared httpx FeatureChain when co-mounted; this package owns RBAC and audit. The orchestrator
// adds "cost" and "budgets" to the feature-prefix list + featureLabels and wires Deps here.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, audit: d.Audit, log: log}

	mux.HandleFunc("GET "+costBase+"/summary", h.read(rbac.CostRead, h.summary))
	mux.HandleFunc("GET "+costBase+"/per-enrichment", h.read(rbac.CostRead, h.perEnrichment))
	mux.HandleFunc("GET "+costBase+"/roi", h.read(rbac.CostRead, h.roi))
	mux.HandleFunc("GET "+costBase+"/forecast", h.read(rbac.CostRead, h.forecast))
	mux.HandleFunc("GET "+costBase+"/export", h.read(rbac.BudgetsWrite, h.export))
	mux.HandleFunc("GET "+budgetsBase, h.read(rbac.CostRead, h.listBudgets))
	mux.HandleFunc("PUT "+budgetsBase, h.write(rbac.BudgetsWrite, h.putBudgets))
}

// --- middleware ---

func (h *handlers) read(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

func (h *handlers) write(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

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

// --- handlers ---

func (h *handlers) summary(w http.ResponseWriter, r *http.Request) {
	req, ok := h.parseGroupReq(w, r)
	if !ok {
		return
	}
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return
	}
	limit := parseLimit(r)
	rows, next, err := h.svc.Summary(r.Context(), req.groupBy, req.from, req.to, req.filters, req.isOperator, cur, limit)
	if err != nil {
		h.fail(w, "summary", err)
		return
	}
	h.auditCrossTenant(r, "cost_summary", req)
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, renderRow(groupSpecs[req.groupBy].keyCol, row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_by": req.groupBy, "from": req.from, "to": req.to, "source": "modeled",
		"items": items, "next_cursor": cursorOut(next),
	})
}

func (h *handlers) perEnrichment(w http.ResponseWriter, r *http.Request) {
	// Same query builder as summary; the "per-enrichment" view is just the ratios rendered with
	// their numerator+denominator carried (doc 04 §2.10). No pagination cursor is exposed.
	req, ok := h.parseGroupReq(w, r)
	if !ok {
		return
	}
	rows, _, err := h.svc.Summary(r.Context(), req.groupBy, req.from, req.to, req.filters, req.isOperator, db.Cursor{}, 200)
	if err != nil {
		h.fail(w, "per-enrichment", err)
		return
	}
	h.auditCrossTenant(r, "cost_per_enrichment", req)
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, renderRow(groupSpecs[req.groupBy].keyCol, row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_by": req.groupBy, "from": req.from, "to": req.to, "source": "modeled", "items": items,
	})
}

func (h *handlers) roi(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseWindow(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ROI(r.Context(), from, to)
	if err != nil {
		h.fail(w, "roi", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"provider_id": row.ProviderID, "workflow_key": row.WorkflowKey,
			"credits": row.Credits, "fields_filled": row.FieldsFilled,
			"credits_per_field": ratio(row.Credits, row.FieldsFilled),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "source": "modeled", "items": items})
}

func (h *handlers) forecast(w http.ResponseWriter, r *http.Request) {
	horizon := 0
	if v := r.URL.Query().Get("horizon"); v != "" {
		horizon = atoiOr(v, 0)
	}
	f, err := h.svc.Forecast(r.Context(), horizon)
	if err != nil {
		h.fail(w, "forecast", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *handlers) export(w http.ResponseWriter, r *http.Request) {
	req, ok := h.parseGroupReq(w, r)
	if !ok {
		return
	}
	h.auditCrossTenant(r, "cost_export", req)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", "attachment; filename=\"cost-export.ndjson\"")
	w.WriteHeader(http.StatusOK)
	if err := h.svc.Export(r.Context(), w, req.groupBy, req.from, req.to, req.filters, req.isOperator); err != nil {
		// Headers already sent; log and stop. A truncated stream is the honest failure signal.
		h.log.Error("cost export stream failed", "err", err)
	}
}

func (h *handlers) listBudgets(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListBudgets(r.Context())
	if err != nil {
		h.fail(w, "budgets", err)
		return
	}
	if items == nil {
		items = []Budget{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) putBudgets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []Budget `json:"items"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	items, err := h.svc.ReplaceBudgets(r.Context(), body.Items)
	if err != nil {
		h.fail(w, "budgets_put", err)
		return
	}
	if items == nil {
		items = []Budget{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- request parsing ---

type groupReq struct {
	groupBy    string
	from, to   time.Time
	filters    map[string]string
	isOperator bool
}

func (h *handlers) parseGroupReq(w http.ResponseWriter, r *http.Request) (groupReq, bool) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return groupReq{}, false
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "group_by is required")
		return groupReq{}, false
	}
	from, to, ok := parseWindow(w, r)
	if !ok {
		return groupReq{}, false
	}
	req := groupReq{
		groupBy:    groupBy,
		from:       from,
		to:         to,
		filters:    parseFilters(r),
		isOperator: db.RoleFromPrincipal(p) == rbac.RoleOperator,
	}
	return req, true
}

// parseFilters reads filter[dim]=v query parameters into a dim->value map.
func parseFilters(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, vs := range r.URL.Query() {
		if strings.HasPrefix(k, "filter[") && strings.HasSuffix(k, "]") && len(vs) > 0 {
			dim := k[len("filter[") : len(k)-1]
			if dim != "" {
				out[dim] = vs[0]
			}
		}
	}
	return out
}

func parseWindow(w http.ResponseWriter, r *http.Request) (time.Time, time.Time, bool) {
	fromS := r.URL.Query().Get("from")
	toS := r.URL.Query().Get("to")
	if fromS == "" || toS == "" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "from and to (RFC3339) are required")
		return time.Time{}, time.Time{}, false
	}
	from, err1 := time.Parse(time.RFC3339, fromS)
	to, err2 := time.Parse(time.RFC3339, toS)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "from/to must be RFC3339 timestamps")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		return atoiOr(v, 0)
	}
	return 0
}

// auditCrossTenant records an operator cross-Tenant cost read (ADR-0020). It is best-effort — a
// failed audit append is logged, never fatal to a read.
func (h *handlers) auditCrossTenant(r *http.Request, action string, req groupReq) {
	if !req.isOperator || h.audit == nil {
		return
	}
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		return
	}
	if err := h.audit.Append(r.Context(), audit.Entry{
		Action: action, ObjectKind: "cost", ObjectID: req.groupBy,
		ActorUserID: p.UserID, ActorRole: rbac.RoleOperator,
	}); err != nil {
		h.log.Warn("cost cross-tenant audit append failed", "action", action, "err", err)
	}
}

// --- error mapping + small response helpers ---

func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrInvalidGroupBy):
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "group_by is not in the allowed set")
	case errors.Is(err, ErrInvalidFilter):
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "filter dimension is not allowed for this group_by")
	case errors.Is(err, ErrKeyGroupByForbidden):
		writeError(w, http.StatusForbidden, codeForbidden, "group_by=key is operator-only")
	case errors.Is(err, ErrWindowOutOfRange):
		writeError(w, http.StatusBadRequest, codeWindowOutOfRange, "requested window is out of range")
	case errors.Is(err, ErrInvalidBudget):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, err.Error())
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("cost handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
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
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

func cursorOut(c db.Cursor) *string {
	if len(c.K) == 0 && c.ID == "" {
		return nil
	}
	s := db.EncodeCursor(c)
	return &s
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
