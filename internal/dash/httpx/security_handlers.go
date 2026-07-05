package httpx

import (
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// auditItem is the JSON projection of an audit_log row (before/after are opaque snapshots).
type auditItem struct {
	Action      string `json:"action"`
	ObjectKind  string `json:"object_kind,omitempty"`
	ObjectID    string `json:"object_id,omitempty"`
	ActorUserID string `json:"actor_user_id,omitempty"`
	ActorRole   string `json:"actor_role,omitempty"`
	IP          string `json:"ip,omitempty"`
}

// handleAuditList returns the hash-chained audit trail, newest first (doc 04 §2.12).
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	cur, ok := parseCursor(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	entries, next, err := s.audit.List(r.Context(), cur, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "audit list failed")
		return
	}
	out := make([]auditItem, 0, len(entries))
	for _, e := range entries {
		out = append(out, toAuditItem(e))
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: cursorOut(encodeCursor(next))})
}

func toAuditItem(e audit.Entry) auditItem {
	return auditItem{
		Action: e.Action, ObjectKind: e.ObjectKind, ObjectID: e.ObjectID,
		ActorUserID: e.ActorUserID, ActorRole: e.ActorRole, IP: e.IP,
	}
}

// handleAuditVerify walks and verifies the caller tenant's chain (doc 04 §2.12).
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	ok, brokenSeq, err := s.audit.Verify(r.Context(), p.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "verify failed")
		return
	}
	resp := map[string]any{"ok": ok}
	if !ok {
		resp["broken_seq"] = brokenSeq
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAccessList returns the API access log for the caller tenant, newest first (doc 04 §2.12).
func (s *Server) handleAccessList(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if limit == 0 {
		limit = 50
	}
	out := make([]map[string]any, 0, limit)
	err := s.store.Tx(r.Context(), func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select method, route, status, dur_ms, user_id, ip, created_at
			 from api_access_log order by created_at desc, id desc limit $1`, int64(limit))
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, map[string]any{
				"method":     deref(row[0]),
				"route":      deref(row[1]),
				"status":     deref(row[2]),
				"dur_ms":     deref(row[3]),
				"user_id":    deref(row[4]),
				"ip":         deref(row[5]),
				"created_at": deref(row[6]),
			})
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "access log query failed")
		return
	}
	writeJSON(w, http.StatusOK, listEnvelope{Items: out, NextCursor: nil})
}

func deref(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
