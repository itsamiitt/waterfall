package httpx

import (
	"context"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// ctxKeyAuditThread carries the per-request self-audit signal (a mutable holder the wrapper
// installs before the handler runs, so the handler can flip it — a child context value could not
// propagate back up).
type ctxKeyAuditThread struct{}

// auditThread is the wrapper-installed handle a handler uses to signal that it appended its own
// audit row inside its business transaction. It is a pointer in the context so the flip is visible
// to the audited() wrapper after the handler returns.
type auditThread struct {
	selfAudited bool
	conn        *pg.Conn // optional: the open, tenant-bound conn the handler threaded (see WithAuditConn)
}

// WithAuditConn threads an already-open, tenant-bound *pg.Conn into ctx so a handler running inside
// a db.Store transaction can append its audit row in the SAME transaction as its business write via
// audit.Log.AppendConn(ctx, conn, entry) — the resolution of Deviation D-P0-3 (doc 05 §8.1's
// same-transaction guarantee). It returns the same ctx when no audited() wrapper is active. A
// handler that self-audits MUST also call MarkAuditDone so the wrapper does not append a second,
// separate-transaction row.
func WithAuditConn(ctx context.Context, c *pg.Conn) context.Context {
	if at, ok := ctx.Value(ctxKeyAuditThread{}).(*auditThread); ok {
		at.conn = c
	}
	return ctx
}

// AuditConnFrom returns the conn a handler threaded via WithAuditConn, or nil.
func AuditConnFrom(ctx context.Context) *pg.Conn {
	if at, ok := ctx.Value(ctxKeyAuditThread{}).(*auditThread); ok {
		return at.conn
	}
	return nil
}

// MarkAuditDone signals that the handler already appended its audit row in its own transaction (via
// audit.Log.AppendConn), so the audited() wrapper MUST NOT append a second row. This is how a
// same-transaction handler and the shared audited() wrapper coexist without double-auditing: the
// write and its audit row commit or roll back together, and the wrapper becomes a no-op for that
// request. Handlers that do not call this keep the backward-compatible behaviour — the wrapper
// appends in its own transaction immediately after a 2xx (each Append is atomic and per-tenant
// serialized, so the chain still verifies).
func MarkAuditDone(ctx context.Context) {
	if at, ok := ctx.Value(ctxKeyAuditThread{}).(*auditThread); ok {
		at.selfAudited = true
	}
}

// audited wraps a mutating handler so that, on a 2xx outcome, it appends one row to the per-tenant
// audit hash chain (doc 05 §8). The handler may enrich the row via RecordAudit(ctx, objectID,
// after), or — for the same-transaction guarantee (doc 05 §8.1) — append its own row inside its
// business transaction with audit.Log.AppendConn and call MarkAuditDone(ctx), in which case this
// wrapper appends nothing. When the handler does not self-audit, the append runs in its own
// transaction immediately after the handler succeeds; the chain still verifies since each Append is
// atomic and per-tenant serialized.
func (s *Server) audited(action, kind string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info := &auditInfo{}
		at := &auditThread{}
		ctx := context.WithValue(r.Context(), ctxKeyAudit{}, info)
		ctx = context.WithValue(ctx, ctxKeyAuditThread{}, at)
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		h(sw, r.WithContext(ctx))
		if sw.status < 200 || sw.status >= 300 {
			return
		}
		if at.selfAudited {
			return // handler appended its audit row in its own transaction — no double audit
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
