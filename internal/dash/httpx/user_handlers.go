package httpx

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/tenant"
)

var validRoles = map[string]bool{
	rbac.RoleOperator: true, rbac.RoleTenantAdmin: true, rbac.RoleTenantUser: true,
}

// handleUsersList lists users in the caller's tenant scope, keyset-paginated (doc 04 §2.2).
func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	users, next, err := s.users.List(r.Context(), cur, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "list failed")
		return
	}
	out := make([]userSummary, 0, len(users))
	for _, u := range users {
		out = append(out, toUserSummary(u))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: cursorOut(encodeCursor(next))})
}

// handleUserCreate invites a user (doc 04 §2.2): 201; password set via the reset flow when absent.
func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var body userCreateRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Email == "" || !validRoles[body.Role] {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "email and a valid role are required")
		return
	}
	pw := body.Password
	if pw == "" {
		pw = security.NewSeedString() // random, unusable until reset
	}
	hash, err := security.HashPassword(pw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "create failed")
		return
	}
	id, err := s.users.Create(r.Context(), body.Email, hash, body.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "create failed")
		return
	}
	RecordAudit(r.Context(), id, map[string]string{"email": body.Email, "role": body.Role})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "email": body.Email, "role": body.Role})
}

// handleUserGet returns one user (404 across tenants).
func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request) {
	usr, err := s.users.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, security.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, toUserSummary(usr))
}

// handleUserPatch updates role/status (doc 04 §2.2).
func (s *Server) handleUserPatch(w http.ResponseWriter, r *http.Request) {
	var body userPatchRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Role != "" && !validRoles[body.Role] {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "invalid role")
		return
	}
	id := r.PathValue("id")
	err := s.users.UpdateRoleStatus(r.Context(), id, body.Role, body.Status)
	if errors.Is(err, security.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "update failed")
		return
	}
	RecordAudit(r.Context(), id, map[string]string{"role": body.Role, "status": body.Status})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

// handleUserDelete soft-deactivates a user and revokes its sessions.
func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.users.Deactivate(r.Context(), id)
	if errors.Is(err, security.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "deactivate failed")
		return
	}
	RecordAudit(r.Context(), id, map[string]string{"status": "deactivated"})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deactivated"})
}

// handleResetPassword issues a password reset (invalidates sessions).
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	hash, err := security.HashPassword(security.NewSeedString())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "reset failed")
		return
	}
	if err := s.users.ResetPassword(r.Context(), id, hash); errors.Is(err, security.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "reset failed")
		return
	}
	RecordAudit(r.Context(), id, map[string]string{"action": "password_reset"})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "reset_issued"})
}

// handleRoles serves the static role×action matrix as data (doc 04 §2.2, §1.2): the SPA mirrors,
// never replaces, this server authority.
func (s *Server) handleRoles(w http.ResponseWriter, _ *http.Request) {
	roles := []string{rbac.RoleOperator, rbac.RoleTenantAdmin, rbac.RoleTenantUser}
	matrix := map[string]map[string]string{}
	for _, a := range allActions {
		row := map[string]string{}
		for _, role := range roles {
			row[role] = rbac.Can(role, a).String()
		}
		matrix[string(a)] = row
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles, "matrix": matrix})
}

// handleIPList returns the tenant's CIDR allowlist.
func (s *Server) handleIPList(w http.ResponseWriter, r *http.Request) {
	rules, err := s.ipallow.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "list failed")
		return
	}
	out := make([]map[string]string, 0, len(rules))
	for _, ru := range rules {
		out = append(out, map[string]string{"cidr": ru.CIDR, "label": ru.Label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

// handleIPPut full-replaces the allowlist with a lockout guard: the caller's current IP must fall
// inside the new set, else 422 validation_failed (doc 05 §6).
func (s *Server) handleIPPut(w http.ResponseWriter, r *http.Request) {
	var body ipAllowlistRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	rules := make([]security.IPRule, 0, len(body.Rules))
	for _, ru := range body.Rules {
		rules = append(rules, security.IPRule{CIDR: ru.CIDR, Label: ru.Label})
	}
	// Lockout guard: a non-empty new set must include the caller's effective address.
	if len(rules) > 0 && !security.CIDRsContain(rules, s.clientIP(r)) {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed,
			"new allowlist would lock out the caller's current address")
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if err := s.ipallow.Replace(r.Context(), rules, p.UserID); err != nil {
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, "invalid CIDR in allowlist")
		return
	}
	RecordAudit(r.Context(), "", map[string]string{"count": strconv.Itoa(len(rules))})
	writeJSON(w, http.StatusOK, map[string]string{"status": "replaced"})
}
