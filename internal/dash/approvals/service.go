package approvals

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Auditor is the consumer-side view of the per-tenant hash-chained audit log (satisfied by
// *audit.Log). Every approval transition chains its row in the SAME transaction as the state change
// via AppendConn (doc 05 §8.1), so a decision and its audit row commit or roll back together.
type Auditor interface {
	AppendConn(ctx context.Context, c *pg.Conn, e audit.Entry) error
}

var _ Auditor = (*audit.Log)(nil)

// TenantSource enumerates the tenants the expirer loop sweeps (RLS requires a bound tenant per
// UPDATE). Optional; when nil the loop sweeps only the platform tenant. The orchestrator wires a
// reader over the tenants table.
type TenantSource interface {
	ActiveTenantIDs(ctx context.Context) ([]string, error)
}

// Config bundles the Service's collaborators.
type Config struct {
	Store          *db.Store
	Audit          Auditor
	Roster         Roster       // optional deadlock guard (doc 05 §9.1)
	Tenants        TenantSource // optional; expirer sweep scope
	Now            func() time.Time
	Logger         *slog.Logger
	ExpireInterval time.Duration // expirer tick; default 1m
}

// Service is the approvals engine. It implements Gate (Check) and owns the decision lifecycle,
// exactly-once execution via a per-action_kind Executor registry, and the background expirer loop.
type Service struct {
	store    *db.Store
	audit    Auditor
	roster   Roster
	tenants  TenantSource
	now      func() time.Time
	log      *slog.Logger
	interval time.Duration

	mu        sync.RWMutex
	executors map[string]Executor

	startOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
}

// NewService builds the approvals Service from cfg. The orchestrator then RegisterExecutor's one
// Executor per gated action_kind, wires the Service as the Gate into providers/keys/configver, and
// calls Start() to run the expirer.
func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ExpireInterval <= 0 {
		cfg.ExpireInterval = time.Minute
	}
	return &Service{
		store:     cfg.Store,
		audit:     cfg.Audit,
		roster:    cfg.Roster,
		tenants:   cfg.Tenants,
		now:       cfg.Now,
		log:       cfg.Logger,
		interval:  cfg.ExpireInterval,
		executors: map[string]Executor{},
	}
}

// RegisterExecutor binds an Executor to an action_kind. The orchestrator calls this once per gated
// action_kind at startup (e.g. provider_delete -> providers.Service.Delete). approvals invokes the
// Executor EXACTLY ONCE on quorum with the pinned payload.
func (s *Service) RegisterExecutor(actionKind string, fn Executor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[actionKind] = fn
}

func (s *Service) executorFor(actionKind string) Executor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.executors[actionKind]
}

// --- Gate.Check: create the pinned request, return proceed=false + requestID ---

// Check implements Gate. It resolves the fail-closed policy for actionKind, (optionally) runs the
// deadlock guard, and inserts a pending approval_requests row pinning the exact payload bytes to
// execute. It returns proceed=false with the new request id; the gated caller then answers 202
// {approval_request_id}. proceed=true is returned only when a policy defensively sets required=0.
func (s *Service) Check(ctx context.Context, actionKind string, payload json.RawMessage) (bool, string, error) {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return false, "", err // fail-closed: no principal => no gate, no proceed
	}
	if !isKnownActionKind(actionKind) {
		return false, "", ErrUnknownActionKind
	}
	pol, err := s.resolvePolicy(ctx, actionKind)
	if err != nil {
		return false, "", err
	}
	if pol.RequiredApprovals <= 0 {
		return true, "", nil // defensive: a disarmed policy proceeds inline (never under the default)
	}
	if s.roster != nil {
		n, rerr := s.roster.EligibleApprovers(ctx, pol.ApproverRole, p.UserID)
		if rerr != nil {
			return false, "", rerr
		}
		if n < pol.RequiredApprovals {
			return false, "", ErrNoEligibleApprover // never park an un-approvable request
		}
	}
	id := newUUID()
	expiresAt := s.now().Add(time.Duration(pol.ExpiresAfterS) * time.Second)
	err = s.store.Tx(ctx, func(c *pg.Conn) error {
		if ierr := insertRequest(c, id, p.TenantID, actionKind, payload, p.UserID, pol.RequiredApprovals, expiresAt); ierr != nil {
			return ierr
		}
		return s.audit.AppendConn(ctx, c, audit.Entry{
			Action:      "approval_request_create",
			ObjectKind:  "approval_requests",
			ObjectID:    id,
			ActorUserID: p.UserID,
			ActorRole:   db.RoleFromPrincipal(p),
			After:       jraw(map[string]any{"action_kind": actionKind, "status": StatusPending, "required_approvals": pol.RequiredApprovals}),
		})
	})
	if err != nil {
		return false, "", err
	}
	return false, id, nil
}

// CreateRequest is the explicit POST /approvals path (rare — most requests come from gated
// endpoints). It creates the pinned request and returns its full snapshot.
func (s *Service) CreateRequest(ctx context.Context, actionKind string, payload json.RawMessage) (Request, error) {
	_, id, err := s.Check(ctx, actionKind, payload)
	if err != nil {
		return Request{}, err
	}
	return s.GetRequest(ctx, id)
}

// --- decisions ---

// Approve records an approve decision by approverUserID (role approverRole). Four-eyes, approver
// authority, and step-up are enforced; on reaching quorum the pinned payload executes EXACTLY ONCE.
// mfaVerified must be true (the handler proves the X-MFA-Code first; the service re-asserts it
// fail-closed).
func (s *Service) Approve(ctx context.Context, requestID, approverUserID, approverRole, comment string, mfaVerified bool) (Request, error) {
	return s.decide(ctx, requestID, approverUserID, approverRole, DecisionApprove, comment, mfaVerified)
}

// Reject records a reject decision; a single reject moves the request to terminal 'rejected'.
func (s *Service) Reject(ctx context.Context, requestID, approverUserID, approverRole, comment string, mfaVerified bool) (Request, error) {
	return s.decide(ctx, requestID, approverUserID, approverRole, DecisionReject, comment, mfaVerified)
}

// decide is the shared approve/reject body. Pre-tx it enforces the checks that need only a read
// (mfa, four-eyes, approver authority); the transactional part holds SELECT ... FOR UPDATE on the
// request row for the in-tx expiry re-check, the decision insert, the quorum tally, and — on quorum
// — the exactly-once execution.
func (s *Service) decide(ctx context.Context, requestID, approverUserID, approverRole, verb, comment string, mfaVerified bool) (Request, error) {
	pre, err := s.GetRequest(ctx, requestID) // RLS-scoped; ErrNotFound if absent/cross-tenant
	if err != nil {
		return Request{}, err
	}
	if !mfaVerified {
		return Request{}, ErrMFARequired
	}
	if approverUserID == pre.RequestedBy {
		return Request{}, ErrFourEyes // unconditional four-eyes, all roles
	}
	pol, err := s.resolvePolicy(ctx, pre.ActionKind)
	if err != nil {
		return Request{}, err
	}
	if approverRole != pol.ApproverRole {
		return Request{}, ErrApproverRole
	}

	err = s.store.Tx(ctx, func(c *pg.Conn) error {
		rc, found, lerr := lockRequest(c, requestID)
		if lerr != nil {
			return lerr
		}
		if !found {
			return ErrNotFound
		}
		if rc.Status != StatusPending {
			// Late/replayed decision after the request already resolved.
			switch rc.Status {
			case StatusExecuted, StatusApproved, StatusFailed:
				return nil // quorum already reached elsewhere; return the snapshot, no re-execution
			default: // rejected, cancelled, expired
				return ErrNotPending
			}
		}
		// In-tx expiry re-check: a just-expired request can never be decided (doc 05 §9.2).
		if s.now().After(rc.ExpiresAt) {
			if serr := setStatus(c, requestID, StatusExpired); serr != nil {
				return serr
			}
			return ErrExpired
		}
		if derr := insertDecision(c, requestID, rc.TenantID, approverUserID, verb, comment, mfaVerified); derr != nil {
			return derr
		}
		if verb == DecisionReject {
			if verr := s.auditDecision(ctx, c, rc, verb, approverUserID, approverRole); verr != nil {
				return verr
			}
			return setStatus(c, requestID, StatusRejected)
		}
		n, cerr := countApprovals(c, requestID)
		if cerr != nil {
			return cerr
		}
		if n < rc.RequiredApprovals {
			// Quorum not yet met: record the approve decision, stay pending.
			return s.auditDecision(ctx, c, rc, verb, approverUserID, approverRole)
		}
		// Quorum reached: execute EXACTLY ONCE in this locked tx. The Executor runs BEFORE this
		// connection takes the per-tenant audit_chain_heads FOR UPDATE lock — executeLocked (the
		// approval_execute audit + status write) and the winning-decision audit both acquire that
		// lock only AFTER exec returns. This is what lets an Executor whose real service method
		// appends to the SAME tenant's audit chain on its own transaction run without deadlocking
		// against a chain-head lock this decision would otherwise already hold (P4 cross-cutting
		// wiring; see OI-P4-2). Cross-connection execution atomicity is unchanged (doc 05 §9.2): an
		// executor error still parks the request in 'failed'.
		if eerr := s.executeLocked(ctx, c, rc); eerr != nil {
			return eerr
		}
		return s.auditDecision(ctx, c, rc, verb, approverUserID, approverRole)
	})
	if err != nil {
		return Request{}, err
	}
	return s.GetRequest(ctx, requestID)
}

// Cancel moves a pending request to terminal 'cancelled'. The requester or a tenant_admin+ may
// cancel (enforced by the handler's RBAC + this method's caller identity).
func (s *Service) Cancel(ctx context.Context, requestID, cancelUserID, role string) (Request, error) {
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		rc, found, lerr := lockRequest(c, requestID)
		if lerr != nil {
			return lerr
		}
		if !found {
			return ErrNotFound
		}
		if rc.Status != StatusPending {
			return ErrNotPending
		}
		if serr := setStatus(c, requestID, StatusCancelled); serr != nil {
			return serr
		}
		return s.auditDecision(ctx, c, rc, "cancel", cancelUserID, role)
	})
	if err != nil {
		return Request{}, err
	}
	return s.GetRequest(ctx, requestID)
}

// executeLocked runs the registered Executor for rc.ActionKind exactly once against the pinned
// payload, then records the terminal status + execution_result IN THE SAME locked transaction, so
// the quorum decision and its outcome commit atomically. An executor error is NOT propagated to the
// tx (which would roll back the whole decision) — it is captured into execution_result and the
// request parks in 'failed' for triage (doc 05 §9.2). The request id rides ctx so the executor's
// underlying service method can use it as its Idempotency-Key.
func (s *Service) executeLocked(ctx context.Context, c *pg.Conn, rc requestCore) error {
	exec := s.executorFor(rc.ActionKind)
	if exec == nil {
		if aerr := s.auditExec(ctx, c, rc, StatusFailed); aerr != nil {
			return aerr
		}
		return setExecuted(c, rc.ID, StatusFailed, failResult(ErrNoExecutor.Error()), s.now())
	}
	ectx := WithRequestID(ctx, rc.ID)
	if execErr := exec(ectx, rc.Payload); execErr != nil {
		if aerr := s.auditExec(ctx, c, rc, StatusFailed); aerr != nil {
			return aerr
		}
		return setExecuted(c, rc.ID, StatusFailed, failResult(execErr.Error()), s.now())
	}
	if aerr := s.auditExec(ctx, c, rc, StatusExecuted); aerr != nil {
		return aerr
	}
	return setExecuted(c, rc.ID, StatusExecuted, okResult(s.now()), s.now())
}

// --- expirer loop ---

// Start launches the background expirer loop (idempotent; a second call is a no-op).
func (s *Service) Start() {
	s.startOnce.Do(func() {
		s.stop = make(chan struct{})
		s.wg.Add(1)
		go s.expirerLoop()
	})
}

// Stop halts the expirer loop and waits for it to drain.
func (s *Service) Stop() {
	if s.stop != nil {
		close(s.stop)
		s.wg.Wait()
	}
}

func (s *Service) expirerLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if n, err := s.sweepExpired(context.Background()); err != nil {
				s.log.Warn("approval expirer sweep failed", "err", err)
			} else if n > 0 {
				s.log.Info("approval expirer swept", "expired", n)
			}
		}
	}
}

// --- audit helpers (chained in-tx on the caller's connection) ---

func (s *Service) auditDecision(ctx context.Context, c *pg.Conn, rc requestCore, verb, userID, role string) error {
	return s.audit.AppendConn(ctx, c, audit.Entry{
		Action:      "approval_" + verb,
		ObjectKind:  "approval_requests",
		ObjectID:    rc.ID,
		ActorUserID: userID,
		ActorRole:   role,
		After:       jraw(map[string]any{"action_kind": rc.ActionKind, "decision": verb}),
	})
}

func (s *Service) auditExec(ctx context.Context, c *pg.Conn, rc requestCore, status string) error {
	return s.audit.AppendConn(ctx, c, audit.Entry{
		Action:      "approval_execute",
		ObjectKind:  "approval_requests",
		ObjectID:    rc.ID,
		ActorUserID: rc.RequestedBy,
		After:       jraw(map[string]any{"action_kind": rc.ActionKind, "status": status}),
	})
}

// --- execution_result shapes ---

func okResult(now time.Time) json.RawMessage {
	return jraw(map[string]any{"status": "ok", "executed_at": now.UTC().Format(time.RFC3339Nano)})
}

func failResult(reason string) json.RawMessage {
	return jraw(map[string]any{"status": "failed", "error": reason})
}

func jraw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
