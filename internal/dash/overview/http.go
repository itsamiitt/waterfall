package overview

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

const basePath = "/v1/admin"

// Error codes (doc 04 §1.6 registry subset; same envelope shape as the shared httpx writer —
// writeError is unexported there, so this package emits the identical shape to avoid a cycle).
const (
	codeInvalidFilter = "invalid_filter"
	codeUnauthorized  = "unauthorized"
	codeForbidden     = "forbidden"
	codeNotFound      = "not_found"
	codeInternal      = "internal"
)

// Authenticator resolves a request into a verified Principal (satisfied by
// httpx.CtxAuthenticator behind the shared FeatureChain).
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// auditor is the consumer-side slice of audit.Log for operator cross-tenant search reads
// (ADR-0020). Satisfied by *audit.Log; nil-safe.
type auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

// Deps bundles the overview surface collaborators.
type Deps struct {
	Aggregator *Aggregator
	Store      *db.Store
	Auth       Authenticator
	Audit      auditor
	Logger     *slog.Logger
}

type handlers struct {
	agg   *Aggregator
	store *db.Store
	auth  Authenticator
	audit auditor
	log   *slog.Logger
}

// Routes mounts the doc 04 §2.13 endpoints: GET /overview, GET /overview/tiles/{tile},
// GET /search, GET /meta/enums. All GETs behind the shared FeatureChain (session auth; not
// CSRF-gated); this package owns RBAC (rbac.OverviewRead — TU+ per the doc 05 matrix; the
// overview payload is v1 operator-scoped platform aggregates, doc 12 §P7).
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{agg: d.Aggregator, store: d.Store, auth: d.Auth, audit: d.Audit, log: log}
	mux.HandleFunc("GET "+basePath+"/overview", h.read(h.overview))
	mux.HandleFunc("GET "+basePath+"/overview/tiles/{tile}", h.read(h.tile))
	mux.HandleFunc("GET "+basePath+"/search", h.read(h.search))
	mux.HandleFunc("GET "+basePath+"/meta/enums", h.read(h.enums))
}

func (h *handlers) read(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, err := tenant.FromContext(ctx); err != nil {
			if h.auth == nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			p, aerr := h.auth.Authenticate(r)
			if aerr != nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			ctx = tenant.WithPrincipal(ctx, p)
		}
		p, _ := tenant.FromContext(ctx)
		if !rbac.Can(db.RoleFromPrincipal(p), rbac.OverviewRead).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		next(w, r.WithContext(ctx))
	}
}

// overview serves the full tile snapshot — the aggregator's last 2s tick, never recomputed
// per request (doc 04 §2.13).
func (h *handlers) overview(w http.ResponseWriter, r *http.Request) {
	snap, err := h.agg.Snapshot(r.Context())
	if err != nil {
		h.log.Error("overview snapshot", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(snap)
}

// tile serves one tile + its drill pointer (deep-link target; unknown tile -> 404).
func (h *handlers) tile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tile")
	if !ValidTile(id) {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown tile")
		return
	}
	snap, err := h.agg.Snapshot(r.Context())
	if err != nil {
		h.log.Error("overview snapshot", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	var doc struct {
		GeneratedAt string                     `json:"generated_at"`
		Tiles       map[string]json.RawMessage `json:"tiles"`
	}
	if err := json.Unmarshal(snap, &doc); err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	body := map[string]any{
		"tile":         id,
		"generated_at": doc.GeneratedAt,
		"data":         doc.Tiles[id],
	}
	if d := Drills[id]; d.Route != "" {
		body["drill"] = d
	} else {
		body["drill"] = nil // system_health: the single non-navigating tile (doc 09 §1.3)
	}
	writeJSON(w, http.StatusOK, body)
}

// --- shared response helpers (identical envelope to httpx) ---

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
