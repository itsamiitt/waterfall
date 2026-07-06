package overview

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// searchKind describes one cross-entity search family (doc 04 §2.13): kinds are walked in
// this FIXED order, each contributing at most min(limit, 20) rows. Every query runs under the
// CALLER's dual-GUC transaction, so RLS scopes rows exactly as the caller's role permits
// (tenants see their catalog projection / own rows; operators see cross-tenant per the
// enumerated SELECT policies — audited below per ADR-0020).
type searchKind struct {
	kind string
	link string // SPA route prefix for the result link
	sql  string // $1 = ILIKE pattern, $2 = per-kind cap; selects (id, label, match_field)
}

var searchKinds = []searchKind{
	{"provider", "/providers/",
		`select id, coalesce(display_name, id),
		   case when id ilike $1 then 'id' else 'name' end
		 from providers where id ilike $1 or display_name ilike $1 order by id limit $2`},
	{"key", "/keys?query=",
		`select id::text, coalesce(label, '****' || coalesce(secret_last4, '')),
		   case when coalesce(label,'') ilike $1 then 'label' else 'last4' end
		 from provider_keys where label ilike $1 or secret_last4 ilike $1
		 order by id limit $2`},
	{"pool", "/key-pools/",
		`select id::text, name, 'name' from key_pools where name ilike $1
		 order by id limit $2`},
	{"workflow", "/workflows/",
		`select scope_key, name,
		   case when scope_key ilike $1 then 'scope' else 'name' end
		 from workflow_index where scope_key ilike $1 or name ilike $1
		 order by scope_key limit $2`},
	{"worker", "/workers?query=",
		`select id, id, 'id' from workers where id ilike $1 order by id limit $2`},
	{"queue", "/queues/",
		`select name, name, 'name' from queue_defs where name ilike $1
		 order by name limit $2`},
	{"user", "/security/users?query=",
		`select id::text, email, 'email' from users where email ilike $1
		 order by id limit $2`},
}

type searchItem struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Label      string `json:"label"`
	MatchField string `json:"match_field"`
	Link       string `json:"link"`
}

// search is GET /v1/admin/search?q=&limit= — bounded cross-entity search for the P8 top bar.
// Per-kind result cap min(limit, 20); response is the §1.4 envelope (items + next_cursor;
// the single page is bounded at 7×20 rows, so next_cursor is null in v1).
func (h *handlers) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "q is required")
		return
	}
	if len(q) > 200 {
		q = q[:200]
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be 1..200")
			return
		}
		limit = n
	}
	perKind := limit
	if perKind > 20 {
		perKind = 20
	}
	pattern := likePattern(q)

	items := []searchItem{}
	err := h.store.Tx(r.Context(), func(c *pg.Conn) error {
		for _, k := range searchKinds {
			res, qerr := c.QueryParams(k.sql, pattern, perKind)
			if qerr != nil {
				return qerr
			}
			for _, row := range res.Rows {
				id := str(row[0])
				items = append(items, searchItem{
					Kind: k.kind, ID: id, Label: str(row[1]),
					MatchField: str(row[2]), Link: k.link + id,
				})
			}
		}
		return nil
	})
	if err != nil {
		h.log.Error("search failed", "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	h.auditOperatorSearch(r)
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

// auditOperatorSearch records an operator's cross-tenant search read (ADR-0020: every handler
// serving rows under an operator cross-tenant SELECT policy audits). Best-effort.
func (h *handlers) auditOperatorSearch(r *http.Request) {
	p, err := tenant.FromContext(r.Context())
	if err != nil || h.audit == nil || db.RoleFromPrincipal(p) != rbac.RoleOperator {
		return
	}
	if err := h.audit.Append(r.Context(), audit.Entry{
		Action: "search", ObjectKind: "search", ObjectID: "cross_entity",
		ActorUserID: p.UserID, ActorRole: rbac.RoleOperator,
	}); err != nil {
		h.log.Warn("search cross-tenant audit append failed", "err", err)
	}
}

// likePattern builds a safe ILIKE '%...%' pattern (escaping \, %, _).
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}
