package keys

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/tenant"
)

// HTTP surface for module 3 (doc 04 §2.4): the 18 Provider Key / import / bulk / Key Pool
// endpoints under /v1/admin. Routes mounts fully-wrapped handlers so an orchestrator only calls
// Routes(mux, deps) — authentication (via the injected Authenticator), RBAC (rbac.Can),
// Idempotency-Key enforcement on writes, and §5.4 step-up (via the optional StepUpVerifier) are
// applied here; audit and RLS are enforced in the service/persistence layers. This package never
// imports internal/dash/httpx, so the two surfaces stay decoupled (coordinate avoidance).

const (
	basePath     = "/v1/admin"
	maxBodyBytes = 1 << 20 // 1 MiB for JSON writes (doc 04 §1.1); import bodies use maxImportBytes
)

// Error codes (doc 04 §1.6 registry subset used by this module).
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeUnauthorized     = "unauthorized"
	codeMFARequired      = "mfa_required"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codePayloadTooLarge  = "payload_too_large"
	codeInternal         = "internal"
)

// Authenticator binds the verified Principal for a request (satisfied by httpx.SessionOrJWT). The
// keys package depends only on this narrow contract, never on the session/JWT internals.
type Authenticator interface {
	Authenticate(*http.Request) (tenant.Principal, error)
}

// StepUpVerifier verifies a per-request X-MFA-Code (§5.4 step-up). Optional: when nil, step-up is
// assumed to be enforced by an outer middleware (the orchestrator's httpx chain).
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, code string) error
}

// Deps are the constructed dependencies Routes needs.
type Deps struct {
	Store   *db.Store
	Secrets secrets.Backend
	Audit   *audit.Log
	Auth    Authenticator
	StepUp  StepUpVerifier // optional (§5.4)
	Logger  *slog.Logger
}

type router struct {
	svc    *Service
	auth   Authenticator
	stepUp StepUpVerifier
	log    *slog.Logger
}

// Routes constructs the module's Service and mounts every endpoint on mux. It is the single entry
// point the orchestrator wires.
func Routes(mux *http.ServeMux, d Deps) {
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	rt := &router{
		svc:    NewService(d.Store, d.Secrets, d.Audit, logger),
		auth:   d.Auth,
		stepUp: d.StepUp,
		log:    logger,
	}
	rt.register(mux)
}

func (rt *router) register(mux *http.ServeMux) {
	// Provider Keys.
	mux.HandleFunc("GET "+basePath+"/providers/{id}/keys", rt.read(rbac.KeysRead, rt.listKeys))
	mux.HandleFunc("POST "+basePath+"/providers/{id}/keys", rt.writeStepUp(rbac.KeysWrite, rt.createKey))
	mux.HandleFunc("GET "+basePath+"/keys/{id}", rt.read(rbac.KeysRead, rt.getKey))
	mux.HandleFunc("PATCH "+basePath+"/keys/{id}", rt.write(rbac.KeysWrite, rt.patchKey))
	mux.HandleFunc("DELETE "+basePath+"/keys/{id}", rt.write(rbac.KeysWrite, rt.deleteKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/enable", rt.write(rbac.KeysWrite, rt.enableKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/disable", rt.write(rbac.KeysWrite, rt.disableKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/rotate", rt.writeStepUp(rbac.KeysWrite, rt.rotateKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/test", rt.write(rbac.KeysWrite, rt.testKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/health-check", rt.write(rbac.KeysWrite, rt.healthCheckKey))
	mux.HandleFunc("POST "+basePath+"/keys/{id}/refresh-credits", rt.write(rbac.KeysWrite, rt.refreshCredits))
	mux.HandleFunc("GET "+basePath+"/keys/{id}/usage", rt.read(rbac.KeysRead, rt.keyUsage))

	// Import + bulk.
	mux.HandleFunc("POST "+basePath+"/providers/{id}/keys/import", rt.writeStepUp(rbac.KeysBulk, rt.importKeys))
	mux.HandleFunc("GET "+basePath+"/key-imports/{job_id}", rt.read(rbac.KeysRead, rt.importStatus))
	mux.HandleFunc("POST "+basePath+"/keys/bulk", rt.write(rbac.KeysBulk, rt.bulkOp))
	mux.HandleFunc("GET "+basePath+"/bulk-jobs/{id}", rt.read(rbac.KeysRead, rt.bulkStatus))

	// Key Pools.
	mux.HandleFunc("GET "+basePath+"/key-pools", rt.read(rbac.KeysRead, rt.listPools))
	mux.HandleFunc("POST "+basePath+"/key-pools", rt.write(rbac.PoolsWrite, rt.createPool))
	mux.HandleFunc("GET "+basePath+"/key-pools/{id}", rt.read(rbac.KeysRead, rt.getPool))
	mux.HandleFunc("PATCH "+basePath+"/key-pools/{id}", rt.write(rbac.PoolsWrite, rt.patchPool))
	mux.HandleFunc("DELETE "+basePath+"/key-pools/{id}", rt.write(rbac.PoolsWrite, rt.deletePool))
	mux.HandleFunc("PUT "+basePath+"/key-pools/{id}/members", rt.write(rbac.PoolsWrite, rt.putMembers))
	mux.HandleFunc("PUT "+basePath+"/key-pools/{id}/strategy", rt.write(rbac.PoolsWrite, rt.putStrategy))
}

// --- middleware composition ---

func (rt *router) read(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(action, h))
}

func (rt *router) write(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(action, rt.requireIdem(h)))
}

func (rt *router) writeStepUp(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
	return rt.authenticate(rt.requireRole(action, rt.requireIdem(rt.requireStepUp(h))))
}

// authenticate binds the Principal (G1) from the injected Authenticator; failure is a uniform 401.
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

// requireRole enforces the RBAC matrix (rbac.Can). ApprovalGated is treated as permitted here.
func (rt *router) requireRole(action rbac.Action, h http.HandlerFunc) http.HandlerFunc {
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
		h(w, r)
	}
}

// requireIdem enforces the Idempotency-Key header on writes (doc 04 §1.3). Durable replay is a
// concern of the httpx idempotency middleware / a later phase (D-P0-2); here we require presence.
func (rt *router) requireIdem(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Idempotency-Key"); k == "" || len(k) > 128 {
			writeError(w, http.StatusBadRequest, codeMissingIdemKey, "the Idempotency-Key header is required on writes")
			return
		}
		h(w, r)
	}
}

// requireStepUp enforces §5.4 re-verification when a StepUpVerifier is wired.
func (rt *router) requireStepUp(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if rt.stepUp != nil {
			code := r.Header.Get("X-MFA-Code")
			if code == "" || rt.stepUp.VerifyStepUp(r.Context(), code) != nil {
				writeError(w, http.StatusUnauthorized, codeMFARequired, "step-up re-verification required")
				return
			}
		}
		h(w, r)
	}
}

// --- handlers: keys ---

func (rt *router) listKeys(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := KeyFilter{
		ProviderID:      r.PathValue("id"),
		Status:          q.Get("status"),
		Health:          q.Get("health"),
		Region:          q.Get("region"),
		Environment:     q.Get("environment"),
		Tag:             q.Get("tag"),
		RotationGroup:   q.Get("rotation_group"),
		ImportedBatchID: q.Get("imported_batch_id"),
		PoolID:          q.Get("pool_id"),
		Q:               q.Get("q"),
	}
	if f.Status != "" && !validStatus[f.Status] {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "unknown status filter")
		return
	}
	items, next, err := rt.svc.ListKeys(r.Context(), f, cur, limit)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	dtos := make([]keyDTO, 0, len(items))
	for _, k := range items {
		dtos = append(dtos, toKeyDTO(k, ""))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: dtos, NextCursor: cursorOut(encodeCursor(next))})
}

func (rt *router) createKey(w http.ResponseWriter, r *http.Request) {
	var req createKeyReq
	if !decodeJSON(w, r, &req) {
		return
	}
	in := CreateKeyInput{
		Label: req.Label, Secret: req.Secret, AuthMethod: req.AuthMethod,
		Region: req.Region, Environment: req.Environment, Team: req.Team, Owner: req.Owner,
		Notes: req.Notes, RotationGroup: req.RotationGroup, ExpiresAt: req.ExpiresAt,
		Weight: req.Weight, Priority: req.Priority, DailyLimit: req.DailyLimit,
		MonthlyLimit: req.MonthlyLimit, RPMLimit: req.RPMLimit, ConcurrencyLimit: req.ConcurrencyLimit,
		Tags: req.Tags, PoolIDs: req.PoolIDs,
		OwnerTenantID: ownerTenantFor(r.Context()),
	}
	k, prefix, err := rt.svc.CreateKey(r.Context(), r.PathValue("id"), in)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toKeyDTO(k, prefix))
}

func (rt *router) getKey(w http.ResponseWriter, r *http.Request) {
	k, prefix, err := rt.svc.GetKey(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toKeyDTO(k, prefix))
}

func (rt *router) patchKey(w http.ResponseWriter, r *http.Request) {
	var req patchKeyReq
	if !decodeJSON(w, r, &req) {
		return
	}
	k, err := rt.svc.PatchKey(r.Context(), r.PathValue("id"), KeyPatch(req))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toKeyDTO(k, ""))
}

func (rt *router) deleteKey(w http.ResponseWriter, r *http.Request) {
	k, err := rt.svc.ArchiveKey(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toKeyDTO(k, ""))
}

func (rt *router) enableKey(w http.ResponseWriter, r *http.Request) {
	k, err := rt.svc.EnableKey(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toKeyDTO(k, ""))
}

func (rt *router) disableKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = optionalJSON(r, &req)
	k, err := rt.svc.DisableKey(r.Context(), r.PathValue("id"), req.Reason)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toKeyDTO(k, ""))
}

func (rt *router) rotateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Secret   string `json:"secret"`
		OverlapS int    `json:"overlap_s"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := rt.svc.RotateKey(r.Context(), r.PathValue("id"), req.Secret, req.OverlapS)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"successor_key_id": res.SuccessorKeyID,
		"old_key_id":       res.OldKeyID,
		"old_key_status":   res.OldKeyStatus,
		"overlap_until":    nullString(res.OverlapUntil),
	})
}

func (rt *router) testKey(w http.ResponseWriter, r *http.Request) {
	k, err := rt.svc.TestKey(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key_id": k.ID, "status": k.Status,
		"note": "P1 records the test intent; live provider.Call egress lands in P2",
	})
}

func (rt *router) healthCheckKey(w http.ResponseWriter, r *http.Request) {
	k, err := rt.svc.HealthCheckKey(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key_id": k.ID, "health": nullString(k.Health),
		"note": "P1 stamps last_health_at; live probe egress lands in P2",
	})
}

func (rt *router) refreshCredits(w http.ResponseWriter, r *http.Request) {
	k, err := rt.svc.RefreshCredits(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key_id": k.ID, "credits_remaining": k.CreditsRemaining})
}

func (rt *router) keyUsage(w http.ResponseWriter, r *http.Request) {
	u, err := rt.svc.KeyUsage(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key_id": u.KeyID, "credits_used": u.CreditsUsed, "credits_remaining": u.CreditsRemaining,
		"series": u.Series, "note": u.Note,
	})
}

// --- handlers: import + bulk ---

func (rt *router) importKeys(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("id")
	source, data, ok := rt.readImport(w, r)
	if !ok {
		return
	}
	if source != "csv" && source != "xlsx" && source != "json" && source != "paste" {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "format must be one of csv|xlsx|json|paste")
		return
	}
	jobID, err := rt.svc.StartImport(r.Context(), providerID, source, data, ownerTenantFor(r.Context()))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

// readImport extracts (source, data) from either a multipart file upload or a JSON paste body,
// enforcing the 25 MiB cap.
func (rt *router) readImport(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "malformed multipart form")
			return "", nil, false
		}
		source := strings.ToLower(strings.TrimSpace(r.FormValue("format")))
		f, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "multipart file part is required")
			return "", nil, false
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "could not read uploaded file")
			return "", nil, false
		}
		if len(data) > maxImportBytes {
			writeError(w, http.StatusBadRequest, codePayloadTooLarge, "import file exceeds the 25 MiB cap")
			return "", nil, false
		}
		return source, data, true
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return "", nil, false
	}
	if len(body) > maxImportBytes {
		writeError(w, http.StatusBadRequest, codePayloadTooLarge, "import body exceeds the 25 MiB cap")
		return "", nil, false
	}
	var req struct {
		Format string `json:"format"`
		Data   string `json:"data"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return "", nil, false
	}
	return strings.ToLower(strings.TrimSpace(req.Format)), []byte(req.Data), true
}

func (rt *router) importStatus(w http.ResponseWriter, r *http.Request) {
	b, err := rt.svc.ImportStatus(r.Context(), r.PathValue("job_id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toImportDTO(b))
}

func (rt *router) bulkOp(w http.ResponseWriter, r *http.Request) {
	var req bulkReq
	if !decodeJSON(w, r, &req) {
		return
	}
	in := BulkInput{IDs: req.IDs, Op: req.Op, Reason: req.Reason, Preview: req.Preview}
	if req.Filter != nil {
		f := KeyFilter{
			ProviderID:      req.Filter.ProviderID,
			Region:          req.Filter.Region,
			Environment:     req.Filter.Environment,
			RotationGroup:   req.Filter.RotationGroup,
			ImportedBatchID: req.Filter.ImportedBatchID,
			Tag:             req.Filter.Tag,
			PoolID:          req.Filter.PoolID,
		}
		if len(req.Filter.Status) > 0 {
			f.Status = req.Filter.Status[0] // single-status match for P1 (doc filter is an array)
		}
		in.Filter = &f
	}
	// op=delete is approval-gated in the RBAC matrix; for P1 it executes inline+audited (OI-KEYS-1).
	jobID, matched, err := rt.svc.BulkOp(r.Context(), in)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	if req.Preview {
		writeJSON(w, http.StatusOK, map[string]int{"matched": matched})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID})
}

func (rt *router) bulkStatus(w http.ResponseWriter, r *http.Request) {
	j, ok := rt.svc.BulkStatus(r.Context(), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "bulk job not found")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// --- handlers: pools ---

func (rt *router) listPools(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, next, err := rt.svc.ListPools(r.Context(), q.Get("provider_id"), q.Get("strategy"), q.Get("owner_tenant_id"), cur, limit)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	dtos := make([]poolDTO, 0, len(items))
	for _, p := range items {
		dtos = append(dtos, toPoolDTO(p))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: dtos, NextCursor: cursorOut(encodeCursor(next))})
}

func (rt *router) createPool(w http.ResponseWriter, r *http.Request) {
	var req poolReq
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := rt.svc.CreatePool(r.Context(), req.ProviderID, req.Name, req.Strategy, rawToString(req.StrategyParams), ownerTenantFor(r.Context()))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPoolDTO(p))
}

func (rt *router) getPool(w http.ResponseWriter, r *http.Request) {
	p, err := rt.svc.GetPool(r.Context(), r.PathValue("id"))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPoolDTO(p))
}

func (rt *router) patchPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name           *string         `json:"name"`
		Status         *string         `json:"status"`
		StrategyParams json.RawMessage `json:"strategy_params"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var params *string
	if len(req.StrategyParams) > 0 {
		s := rawToString(req.StrategyParams)
		params = &s
	}
	p, err := rt.svc.PatchPool(r.Context(), r.PathValue("id"), req.Name, req.Status, params)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPoolDTO(p))
}

func (rt *router) deletePool(w http.ResponseWriter, r *http.Request) {
	if err := rt.svc.DeletePool(r.Context(), r.PathValue("id")); err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (rt *router) putMembers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyIDs []string `json:"key_ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := rt.svc.PutMembers(r.Context(), r.PathValue("id"), req.KeyIDs)
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPoolDTO(p))
}

func (rt *router) putStrategy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Strategy       string          `json:"strategy"`
		StrategyParams json.RawMessage `json:"strategy_params"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := rt.svc.PutStrategy(r.Context(), r.PathValue("id"), req.Strategy, rawToString(req.StrategyParams))
	if err != nil {
		rt.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPoolDTO(p))
}

// --- error mapping ---

func (rt *router) writeServiceError(w http.ResponseWriter, err error) {
	var dup *DuplicateError
	switch {
	case errors.As(err, &dup):
		writeError(w, http.StatusConflict, codeConflict, dup.Error())
	case errors.Is(err, ErrProviderNotFound), errors.Is(err, ErrKeyNotFound), errors.Is(err, ErrPoolNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "resource not found")
	case errors.Is(err, ErrInvalidTransition):
		writeError(w, http.StatusConflict, codeConflict, "illegal key state transition")
	case errors.Is(err, ErrInvalidStrategy):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "unknown pool strategy")
	case errors.Is(err, ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, err.Error())
	case errors.Is(err, errZipBomb), errors.Is(err, errTooManyRows),
		errors.Is(err, errBadXLSX), errors.Is(err, errBadCSV), errors.Is(err, errBadJSON):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, err.Error())
	default:
		rt.log.Error("keys handler error", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// ownerTenantFor returns the BYO owner tenant for a write: "" for platform operators
// (platform-managed rows), else the caller's own Tenant (doc 05 §3.2 service-method BYO path).
func ownerTenantFor(ctx context.Context) string {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return ""
	}
	if db.RoleFromPrincipal(p) == rbac.RoleOperator {
		return ""
	}
	return p.TenantID
}
