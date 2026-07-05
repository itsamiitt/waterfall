package approvals

import (
	"context"
	"sort"
	"strings"

	"github.com/enrichment/waterfall/internal/pg"
)

// TimelineEvent is one row in the Stripe-style /change-history/{kind}/{id} view (doc 04 §2.12): a
// config version transition, an approval request/decision, or an audit-log entry, unified by
// timestamp.
type TimelineEvent struct {
	Source string `json:"source"` // "config_version" | "approval_request" | "audit"
	At     string `json:"at"`
	Action string `json:"action,omitempty"`
	Status string `json:"status,omitempty"`
	Actor  string `json:"actor_user_id,omitempty"`
	Ref    string `json:"ref,omitempty"` // version id / approval request id / audit seq
	Detail string `json:"detail,omitempty"`
}

// ChangeHistory aggregates config_versions + approval_requests + audit_log rows for one object into
// a single time-ordered timeline (read-only, RLS-scoped). It queries the tables directly — no
// import of configver — and degrades gracefully when an optional source table is absent (so it
// works over a minimal schema). audit_log is the backbone (every mutation appends a row keyed by
// object_id); config_versions is included for config kinds; approval_requests are matched by id or
// by a pinned payload id/scope_key.
func (s *Service) ChangeHistory(ctx context.Context, objectKind, objectID string) ([]TimelineEvent, error) {
	var events []TimelineEvent
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		// (1) audit_log — the backbone; always present alongside approvals (migration 0004).
		res, qerr := c.QueryParams(
			`select seq, action, actor_user_id, created_at, object_kind
			   from audit_log where object_id = $1 order by seq asc`, objectID)
		if qerr != nil {
			return qerr
		}
		for _, row := range res.Rows {
			events = append(events, TimelineEvent{
				Source: "audit", At: tsOut(strOf(row[3])), Action: strOf(row[1]),
				Actor: strOf(row[2]), Ref: strOf(row[0]), Detail: strOf(row[4]),
			})
		}

		// (2) approval_requests — matched by request id or a pinned payload id/scope_key.
		res, qerr = c.QueryParams(
			`select id, action_kind, status, created_at, executed_at
			   from approval_requests
			  where id = $1 or payload->>'id' = $1 or payload->>'scope_key' = $1
			  order by created_at asc`, objectID)
		if qerr != nil {
			return qerr
		}
		for _, row := range res.Rows {
			events = append(events, TimelineEvent{
				Source: "approval_request", At: tsOut(strOf(row[3])), Action: strOf(row[1]),
				Status: strOf(row[2]), Ref: strOf(row[0]),
			})
		}

		// (3) config_versions — optional (migration 0006); skipped when the table is absent.
		res, qerr = c.QueryParams(
			`select id, kind, status, version, created_at
			   from config_versions where scope_key = $1 order by created_at asc`, objectID)
		if qerr != nil {
			if isUndefinedTable(qerr) {
				return nil
			}
			return qerr
		}
		for _, row := range res.Rows {
			events = append(events, TimelineEvent{
				Source: "config_version", At: tsOut(strOf(row[4])), Status: strOf(row[2]),
				Ref: strOf(row[0]), Detail: strOf(row[1]) + " v" + strOf(row[3]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
	if events == nil {
		events = []TimelineEvent{}
	}
	return events, nil
}

// isUndefinedTable reports whether err is a Postgres "relation does not exist" error, so
// change-history can skip a source table that a minimal deployment has not migrated.
func isUndefinedTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "does not exist")
}
