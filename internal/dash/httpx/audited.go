package httpx

import (
	"context"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// audited wraps a mutating handler so that, on a 2xx outcome, it appends one row to the per-tenant
// audit hash chain (doc 05 §8). The handler may enrich the row via RecordAudit(ctx, objectID,
// after). For P0 the append runs in its own transaction immediately after the handler succeeds
// (Deviation D-P0-3: doc 05 §8.1's same-transaction guarantee is deferred to the phase that
// threads a *pg.Conn into handlers); the chain still verifies since each Append is atomic and
// per-tenant serialized.
func (s *Server) audited(action, kind string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info := &auditInfo{}
		ctx := context.WithValue(r.Context(), ctxKeyAudit{}, info)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r.WithContext(ctx))
		if sw.status < 200 || sw.status >= 300 {
			return
		}
		p, err := tenant.FromContext(ctx)
		if err != nil {
			return
		}
		e := audit.Entry{
			Action:      action,
			ObjectKind:  kind,
			ObjectID:    info.objectID,
			ActorUserID: p.UserID,
			ActorRole:   db.RoleFromPrincipal(p),
			After:       info.after,
		}
		if rec := recFrom(ctx); rec != nil {
			e.IP = rec.ip
		}
		if err := s.audit.Append(ctx, e); err != nil {
			s.log().Error("audit append failed", "action", action, "kind", kind, "err", err)
		}
	}
}
