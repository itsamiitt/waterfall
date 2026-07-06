package httpx

import (
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/tenant"
)

// tenantPolicy constructs the per-Tenant security-knob seam over the Server's store (SEC-5). It is
// built on demand rather than held as a field so this feature adds no wiring to NewServer/Deps.
func (s *Server) tenantPolicy() *security.TenantPolicy {
	return security.NewTenantPolicy(s.store)
}

// mfaPolicyRequest is the PATCH /v1/admin/settings/mfa-policy body: the single require_mfa toggle
// (SEC-5). A pointer distinguishes an omitted field (422) from an explicit false.
type mfaPolicyRequest struct {
	RequireMFA *bool `json:"require_mfa"`
}

// HandleMFAPolicyGet reads the caller Tenant's require_mfa knob (GET /v1/admin/settings/mfa-policy,
// T2/SEC-5) so the SPA can show the current toggle state. Read-only: RBAC-gated by rbac.MFAPolicyWrite
// (operator/tenant_admin, the same actors who may change it), no step-up, no audit. The value is read
// under the caller's own Tenant binding via TenantPolicy.RequireMFA.
func (s *Server) HandleMFAPolicyGet(w http.ResponseWriter, r *http.Request) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return
	}
	if !rbac.Can(db.RoleFromPrincipal(p), rbac.MFAPolicyWrite).Allowed() {
		writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
		return
	}
	required, err := s.tenantPolicy().RequireMFA(r.Context(), p.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"require_mfa": required})
}

// HandleMFAPolicy sets the caller Tenant's require_mfa knob (PATCH /v1/admin/settings/mfa-policy,
// T2/SEC-5). It is exported so the orchestrator can mount it behind the shared FeatureChain (or the
// P0 authedWrite chain) at that path. The handler is self-contained and safe under either mount:
//
//   - RBAC: rbac.MFAPolicyWrite (operator allow, tenant_admin own-Tenant, tenant_user deny). Checking
//     it here means a FeatureChain mount — which does not run requireRole — is still gated; a
//     duplicate authedWrite requireRole is harmless (same verdict).
//   - Step-up: a fresh X-MFA-Code (TOTP or recovery code) is verified via Users.VerifyStepUp (doc 05
//     §5.4). No mount runs step-up, so there is no double-consumption.
//   - Tenant scope: the write targets the Principal's own Tenant; the tenants RLS WITH CHECK confines
//     it there regardless (a foreign Tenant id would affect zero rows → 404).
//   - Audit: one row is appended in-band and MarkAuditDone signals the audited() wrapper (when the
//     mount uses one) to skip, so there is exactly one audit row either way.
func (s *Server) HandleMFAPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := tenant.FromContext(r.Context())
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
		return
	}
	role := db.RoleFromPrincipal(p)
	if !rbac.Can(role, rbac.MFAPolicyWrite).Allowed() {
		writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
		return
	}

	var body mfaPolicyRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.RequireMFA == nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "require_mfa is required")
		return
	}

	// Step-up (doc 05 §5.4): a per-request TOTP or recovery-code proof, independent of the session's
	// prior mfa_verified_at, gates this security-relevant config change.
	if ok, verr := s.users.VerifyStepUp(r.Context(), p.UserID, r.Header.Get("X-MFA-Code"), s.now()); verr != nil || !ok {
		writeError(w, http.StatusUnauthorized, codeMFARequired, "step-up verification required")
		return
	}

	require := *body.RequireMFA
	if err := s.tenantPolicy().SetRequireMFA(r.Context(), p.TenantID, require); err != nil {
		if err == security.ErrNotFound {
			writeError(w, http.StatusNotFound, codeNotFound, "tenant not found")
			return
		}
		writeError(w, http.StatusInternalServerError, codeInternal, "update failed")
		return
	}

	s.appendAudit(r.Context(), audit.Entry{
		Action: "mfa_policy_update", ObjectKind: "tenants", ObjectID: p.TenantID,
		ActorUserID: p.UserID, ActorRole: role, IP: s.clientIP(r),
		After: jsonStr(map[string]string{"require_mfa": strconv.FormatBool(require)}),
	})
	MarkAuditDone(r.Context())

	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "require_mfa": require})
}
