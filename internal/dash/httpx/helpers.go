package httpx

import (
	"encoding/json"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// jsonStr marshals v (a string/int/bool-valued map) into a json.RawMessage for an audit snapshot.
// Errors are impossible for these shapes; a failure yields nil (an empty snapshot).
func jsonStr(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// roleOf returns the RBAC role carried on a Principal, or "".
func roleOf(p tenant.Principal) string { return db.RoleFromPrincipal(p) }

// splitCookieValue parses a "<tenant>|<session_id>" cookie value.
func splitCookieValue(v string) (tenantID, id string, ok bool) {
	i := strings.IndexByte(v, '|')
	if i <= 0 || i == len(v)-1 {
		return "", "", false
	}
	return v[:i], v[i+1:], true
}
