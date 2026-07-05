package approvals

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// requestCore is the mutable/decision-relevant subset of an approval_requests row, read under the
// FOR UPDATE lock in the decision path.
type requestCore struct {
	ID                string
	TenantID          string
	ActionKind        string
	RequestedBy       string
	Status            string
	RequiredApprovals int
	ExpiresAt         time.Time
	ExecutedAt        *time.Time
	ExecutionResult   json.RawMessage
	Payload           json.RawMessage // the pinned bytes to execute
}

// insertRequest writes the pinned approval_requests row on c. payload is stored as jsonb (the
// exact resolved bytes to execute). expires_at is computed by the caller from the policy.
func insertRequest(c *pg.Conn, id, tenantID, actionKind string, payload json.RawMessage, requestedBy string, required int, expiresAt time.Time) error {
	return c.ExecParams(
		`insert into approval_requests
		   (id, tenant_id, action_kind, payload, requested_by, status, required_approvals, expires_at)
		 values ($1,$2,$3,$4::jsonb,$5,'pending',$6,$7)`,
		id, tenantID, actionKind, string(payload), requestedBy, required, expiresAt)
}

// lockRequest reads the request row FOR UPDATE (serializing concurrent deciders on this request).
// found=false when no such row exists in the caller's tenant scope.
func lockRequest(c *pg.Conn, id string) (rc requestCore, found bool, err error) {
	res, err := c.QueryParams(
		`select id, tenant_id, action_kind, requested_by, status, required_approvals,
		        expires_at, executed_at, execution_result, payload
		   from approval_requests where id = $1 for update`, id)
	if err != nil {
		return requestCore{}, false, err
	}
	if len(res.Rows) == 0 {
		return requestCore{}, false, nil
	}
	row := res.Rows[0]
	rc = requestCore{
		ID:                strOf(row[0]),
		TenantID:          strOf(row[1]),
		ActionKind:        strOf(row[2]),
		RequestedBy:       strOf(row[3]),
		Status:            strOf(row[4]),
		RequiredApprovals: atoiOr(strOf(row[5]), 1),
		ExpiresAt:         parseTS(strOf(row[6])),
		ExecutionResult:   rawFromPtr(row[8]),
		Payload:           rawFromPtr(row[9]),
	}
	if row[7] != nil {
		t := parseTS(*row[7])
		rc.ExecutedAt = &t
	}
	return rc, true, nil
}

// insertDecision records one approval_decisions row, no-op on a duplicate (request_id,
// approver_user_id) so a replayed vote is idempotent rather than a PK error.
func insertDecision(c *pg.Conn, requestID, tenantID, approverUserID, decision, comment string, mfaVerified bool) error {
	return c.ExecParams(
		`insert into approval_decisions
		   (request_id, tenant_id, approver_user_id, decision, comment, mfa_verified)
		 values ($1,$2,$3,$4,$5,$6)
		 on conflict (request_id, approver_user_id) do nothing`,
		requestID, tenantID, approverUserID, decision, nullIf(comment), mfaVerified)
}

// countApprovals counts DISTINCT approvers who voted 'approve' on the request (the quorum tally).
// The PK(request_id, approver_user_id) already makes each approver at most one row, so a plain
// count is a distinct count.
func countApprovals(c *pg.Conn, requestID string) (int, error) {
	res, err := c.QueryParams(
		`select count(*) from approval_decisions where request_id = $1 and decision = 'approve'`, requestID)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return 0, nil
	}
	return atoiOr(*res.Rows[0][0], 0), nil
}

// setStatus transitions a request to a terminal non-execution status (rejected/expired/cancelled).
func setStatus(c *pg.Conn, id, status string) error {
	return c.ExecParams(`update approval_requests set status = $2 where id = $1`, id, status)
}

// setExecuted records the execution outcome: status ('executed' or 'failed'), execution_result, and
// executed_at (only for the successful terminal, mirroring the doc example).
func setExecuted(c *pg.Conn, id, status string, result json.RawMessage, executedAt time.Time) error {
	return c.ExecParams(
		`update approval_requests
		    set status = $2, execution_result = $3::jsonb, executed_at = $4
		  where id = $1`,
		id, status, string(result), executedAt)
}

// --- read paths (RLS-scoped under the caller's Principal) ---

// GetRequest returns one request with its decisions (RLS-scoped). Absent => ErrNotFound.
func (s *Service) GetRequest(ctx context.Context, id string) (Request, error) {
	var out Request
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id, tenant_id, action_kind, payload, requested_by, status, required_approvals,
			        expires_at, executed_at, execution_result, created_at
			   from approval_requests where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		found = true
		out = scanRequest(res.Rows[0])
		decs, err := loadDecisions(c, id)
		if err != nil {
			return err
		}
		out.Decisions = decs
		return nil
	})
	if err != nil {
		return Request{}, err
	}
	if !found {
		return Request{}, ErrNotFound
	}
	return out, nil
}

// loadDecisions reads the decision rows for a request (oldest-first).
func loadDecisions(c *pg.Conn, requestID string) ([]Decision, error) {
	res, err := c.QueryParams(
		`select approver_user_id, decision, comment, mfa_verified, created_at
		   from approval_decisions where request_id = $1 order by created_at asc`, requestID)
	if err != nil {
		return nil, err
	}
	out := make([]Decision, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, Decision{
			ApproverUserID: strOf(row[0]),
			Decision:       strOf(row[1]),
			Comment:        strOf(row[2]),
			MFAVerified:    strOf(row[3]) == "t" || strOf(row[3]) == "true",
			CreatedAt:      tsOut(strOf(row[4])),
		})
	}
	return out, nil
}

// ListRequests returns approval requests newest-first (keyset on created_at,id DESC), bounded by
// db.ClampLimit, optionally filtered by status and/or action_kind (doc 04 §2.12 filters).
func (s *Service) ListRequests(ctx context.Context, status, actionKind string, cur db.Cursor, limit int) ([]Request, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var out []Request
	var next db.Cursor
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		// Keyset: (created_at, id) strictly-less-than the cursor, DESC. Cursor carries K=[created_at]
		// and ID=[id].
		sql := `select id, tenant_id, action_kind, payload, requested_by, status, required_approvals,
		               expires_at, executed_at, execution_result, created_at
		          from approval_requests
		         where ($1 = '' or status = $1)
		           and ($2 = '' or action_kind = $2)`
		args := []any{status, actionKind}
		if len(cur.K) > 0 && cur.K[0] != "" {
			sql += ` and (created_at, id) < ($3, $4)`
			args = append(args, cur.K[0], cur.ID)
		}
		sql += ` order by created_at desc, id desc limit $` + strconv.Itoa(len(args)+1)
		args = append(args, int64(limit+1))
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			last := rows[limit-1]
			next = db.Cursor{K: []string{strOf(last[10])}, ID: strOf(last[0])}
			rows = rows[:limit]
		}
		for _, row := range rows {
			r := scanRequest(row)
			r.Payload = nil // list view omits the (potentially large) pinned payload
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// scanRequest maps a full approval_requests row (the 11-column projection above) to a Request.
func scanRequest(row []*string) Request {
	r := Request{
		ID:                strOf(row[0]),
		TenantID:          strOf(row[1]),
		ActionKind:        strOf(row[2]),
		Payload:           rawFromPtr(row[3]),
		RequestedBy:       strOf(row[4]),
		Status:            strOf(row[5]),
		RequiredApprovals: atoiOr(strOf(row[6]), 1),
		ExpiresAt:         tsOut(strOf(row[7])),
		ExecutionResult:   rawFromPtr(row[9]),
		CreatedAt:         tsOut(strOf(row[10])),
	}
	if row[8] != nil {
		r.ExecutedAt = tsOut(*row[8])
	}
	return r
}

// --- expirer sweep ---

// sweepExpired flips pending requests past expires_at to 'expired', per tenant (RLS requires a
// bound tenant). The UPDATE's WHERE re-checks status='pending' under the same row lock the decision
// path takes via FOR UPDATE, so the sweep can never race a just-completing approval to expiry. When
// no TenantSource is wired it sweeps only the platform tenant (operator-level requests); the
// decision-time in-tx expiry re-check remains authoritative for every tenant regardless.
func (s *Service) sweepExpired(ctx context.Context) (int, error) {
	tenants := []string{"platform"}
	if s.tenants != nil {
		ids, err := s.tenants.ActiveTenantIDs(ctx)
		if err != nil {
			return 0, err
		}
		if len(ids) > 0 {
			tenants = ids
		}
	}
	now := s.now()
	total := 0
	for _, tid := range tenants {
		sctx := tenant.WithPrincipal(ctx, tenant.Principal{
			TenantID: tid, Scopes: []string{"role:tenant_admin"},
		})
		err := s.store.Tx(sctx, func(c *pg.Conn) error {
			res, err := c.QueryParams(
				`update approval_requests set status = 'expired'
				  where status = 'pending' and expires_at < $1 returning id`, now)
			if err != nil {
				return err
			}
			total += len(res.Rows)
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// --- pg-backed Roster (deadlock guard, doc 05 §9.1) ---

// pgRoster counts eligible approvers from the users table under the caller's tenant (RLS-scoped).
type pgRoster struct{ store *db.Store }

// NewRoster returns a Roster over store that counts active users holding a given role in the
// caller's tenant, excluding one user id. The orchestrator wires it into the Service so the gate
// refuses to park an un-approvable request (422) rather than creating one that can only expire.
func NewRoster(store *db.Store) Roster { return &pgRoster{store: store} }

func (r *pgRoster) EligibleApprovers(ctx context.Context, approverRole, excludeUserID string) (int, error) {
	n := 0
	err := r.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select count(*) from users
			  where role = $1 and status = 'active' and id <> $2`, approverRole, excludeUserID)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			n = atoiOr(*res.Rows[0][0], 0)
		}
		return nil
	})
	return n, err
}

// --- small column helpers (kept local so approvals stays self-contained) ---

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rawFromPtr(p *string) json.RawMessage {
	if p == nil {
		return nil
	}
	return json.RawMessage(*p)
}

func atoiOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// parseTS parses a Postgres timestamptz text rendering (or RFC3339) into a time.Time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// tsOut normalizes a Postgres timestamptz text rendering to RFC3339 (UTC) for JSON output; empty
// stays empty.
func tsOut(s string) string {
	if s == "" {
		return ""
	}
	t := parseTS(s)
	if t.IsZero() {
		return s
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
