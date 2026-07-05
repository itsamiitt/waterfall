package httpx

import (
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
)

// allActions is the P0 action surface projected at GET /roles (doc 04 §2.2). The list mirrors the
// rbac matrix keys; the SPA guards derive from it while the server stays authoritative.
var allActions = []rbac.Action{
	rbac.OverviewRead,
	rbac.ProvidersRead, rbac.ProvidersWrite, rbac.ProvidersDelete,
	rbac.KeysRead, rbac.KeysWrite, rbac.KeysBulk, rbac.KeysDelete,
	rbac.PoolsWrite, rbac.RotationWrite,
	rbac.RoutingPublish, rbac.WorkflowsPublish,
	rbac.QueuesRead, rbac.QueuesReplay,
	rbac.WorkersRead, rbac.WorkersActions,
	rbac.CostRead, rbac.BudgetsWrite,
	rbac.AlertsCRUD, rbac.AlertsAck,
	rbac.UsersCRUD, rbac.SessionsRevoke,
	rbac.AuditRead, rbac.AuditVerify, rbac.ApprovalsDecide,
}

// parseCursor decodes ?cursor= (opaque base64url); a bad cursor is 400 invalid_cursor (doc 04 §1.4).
func parseCursor(w http.ResponseWriter, r *http.Request) (db.Cursor, bool) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return db.Cursor{}, false
	}
	return cur, true
}

// parseLimit decodes ?limit=; out-of-range (or non-integer) is 400 invalid_filter (doc 04 §1.4).
// The value is still clamped downstream by db.ClampLimit (default 50, hard cap 200).
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

// encodeCursor renders a non-empty keyset cursor as an opaque token, or "" for the last page.
func encodeCursor(c db.Cursor) string {
	if len(c.K) == 0 && c.ID == "" {
		return ""
	}
	return db.EncodeCursor(c)
}
