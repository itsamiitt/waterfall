// Package approvals is the dashboard's N-of-M approval-quorum engine (doc 04 §5, doc 05 §9,
// migration 0007). It is the human gate in front of the most dangerous writes: a gated caller
// asks the gate to Check an action_kind, the gate pins the fully-resolved payload into an
// approval_requests row and answers "does not proceed, here is the request id", and the caller
// returns 202 {approval_request_id}. Distinct approvers then decide; on quorum the pinned payload
// executes EXACTLY ONCE through a registered Executor.
//
// NO IMPORT CYCLE (the point of the executor registry + Gate seam): this package NEVER imports
// providers / keys / configver. Those packages depend one-way on the Gate interface defined here
// (wired by the orchestrator), and the concrete side effects reach back only through Executors the
// orchestrator registers by action_kind — so approvals has no compile-time dependency on them.
//
// Gates enforced:
//   - Fail-closed policy: absent approval_policies row => the built-in default (required=1) still
//     gates; an explicit row only customizes knobs, never disarms (doc 05 §9.1).
//   - Four-eyes (approver != requester, unconditional, all roles), approver-role authority, TOTP
//     step-up, in-tx expiry re-check, N-of-M distinct-approver quorum (a DB PK constraint).
//   - Exactly-once execution: quorum counted under SELECT ... FOR UPDATE on the request row; the
//     first decision to reach quorum runs the Executor once and records execution_result; every
//     later/replayed final decision sees a terminal status and returns the stored result.
package approvals

import (
	"context"
	"encoding/json"
	"errors"
)

// Action kinds — the closed catalog matching the approval_policies.action_kind CHECK enum
// (migration 0007, doc 05 §9.1). approval_requests.action_kind has no CHECK, but the gate only
// ever issues these six.
const (
	ActionKeyBulkDelete        = "key_bulk_delete"
	ActionProviderDelete       = "provider_delete"
	ActionProviderArchive      = "provider_archive"
	ActionRoutingPublish       = "routing_publish"
	ActionWorkflowPublish      = "workflow_publish"
	ActionSecretsBackendChange = "secrets_backend_change"
)

// AllActionKinds is the closed catalog, in a stable order (used by tests + /meta parity).
var AllActionKinds = []string{
	ActionKeyBulkDelete, ActionProviderDelete, ActionProviderArchive,
	ActionRoutingPublish, ActionWorkflowPublish, ActionSecretsBackendChange,
}

// platformActionKinds are the operator-level actions whose built-in default approver_role is
// 'operator' rather than 'tenant_admin' (doc 05 §9.1; brief §1). An explicit approval_policies row
// may still override the role.
var platformActionKinds = map[string]bool{
	ActionProviderDelete:       true,
	ActionProviderArchive:      true,
	ActionSecretsBackendChange: true,
}

// Request statuses (approval_requests.status CHECK, migration 0007).
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
	StatusExecuted  = "executed"
	StatusFailed    = "failed"
)

// Decision verbs (approval_decisions.decision CHECK).
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// Executor performs the real side effect for an action_kind, against the pinned payload. The
// orchestrator registers one per action_kind (RegisterExecutor); approvals invokes it EXACTLY ONCE
// on quorum. The approval request id is carried in ctx (RequestIDFromContext) so the executor's
// underlying service method can use it as its Idempotency-Key (G2), making the side effect
// idempotent end-to-end.
type Executor func(ctx context.Context, payload json.RawMessage) error

// Gate is the seam the gated callers (providers / keys / configver) depend on — NOT the concrete
// *Service — so nothing they import pulls in approvals' collaborators. On a gated write the caller
// calls Check; proceed=false means "an approval request was created; return 202 {requestID}".
// proceed=true means the action may run inline (only when a policy defensively sets required=0).
type Gate interface {
	Check(ctx context.Context, actionKind string, payload json.RawMessage) (proceed bool, requestID string, err error)
}

// NopGate is the disabled-gating Gate: it always proceeds inline and creates no request. The
// orchestrator wires it when approvals is compiled-out / disabled, so callers keep one code path.
type NopGate struct{}

// Check always proceeds (gating disabled).
func (NopGate) Check(context.Context, string, json.RawMessage) (bool, string, error) {
	return true, "", nil
}

var (
	_ Gate = (*Service)(nil)
	_ Gate = NopGate{}
)

// StepUpVerifier verifies a per-decision X-MFA-Code against the calling approver's TOTP seed
// (doc 05 §5.4). Satisfied by the same *totpStepUp adapter the keys surface uses (VerifyStepUp over
// security.Users.TOTPSeed) — kept an interface so approvals never imports security. Optional at the
// HTTP layer; when nil, step-up is not enforced (dev/test only).
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, code string) error
}

// Roster answers the deadlock guard (doc 05 §9.1): can the request ever be approved? It returns the
// number of DISTINCT users in the caller's tenant who hold approverRole and are NOT excludeUserID.
// Optional; when nil the guard is skipped (an un-approvable request simply expires — fail-closed).
type Roster interface {
	EligibleApprovers(ctx context.Context, approverRole, excludeUserID string) (int, error)
}

// Request is the read model of an approval_requests row.
type Request struct {
	ID                string          `json:"id"`
	TenantID          string          `json:"tenant_id,omitempty"`
	ActionKind        string          `json:"action_kind"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	RequestedBy       string          `json:"requested_by"`
	Status            string          `json:"status"`
	RequiredApprovals int             `json:"required_approvals"`
	ExpiresAt         string          `json:"expires_at,omitempty"`
	ExecutedAt        string          `json:"executed_at,omitempty"`
	ExecutionResult   json.RawMessage `json:"execution_result,omitempty"`
	CreatedAt         string          `json:"created_at,omitempty"`
	Decisions         []Decision      `json:"decisions,omitempty"`
}

// Decision is the read model of an approval_decisions row.
type Decision struct {
	ApproverUserID string `json:"approver_user_id"`
	Decision       string `json:"decision"`
	Comment        string `json:"comment,omitempty"`
	MFAVerified    bool   `json:"mfa_verified"`
	CreatedAt      string `json:"created_at,omitempty"`
}

// Policy is a resolved approval policy (a row, or the built-in fail-closed default).
type Policy struct {
	ActionKind        string
	RequiredApprovals int
	ApproverRole      string
	ExpiresAfterS     int
	Default           bool // true when synthesized (no approval_policies row existed)
}

// Sentinel errors (mapped to HTTP codes by the handler; see http.go).
var (
	ErrNotFound           = errors.New("approvals: request not found")
	ErrFourEyes           = errors.New("approvals: requester cannot approve own request")
	ErrApproverRole       = errors.New("approvals: approver does not hold the required approver_role")
	ErrMFARequired        = errors.New("approvals: step-up mfa required")
	ErrExpired            = errors.New("approvals: request is expired")
	ErrNotPending         = errors.New("approvals: request is not pending")
	ErrNoExecutor         = errors.New("approvals: no executor registered for action_kind")
	ErrNoEligibleApprover = errors.New("approvals: no eligible approver other than the requester")
	ErrCommentRequired    = errors.New("approvals: a justification comment is required")
	ErrUnknownActionKind  = errors.New("approvals: unknown action_kind")
)

// ctxKey is the private context key type carrying the request id into an Executor.
type ctxKey struct{}

// WithRequestID returns ctx carrying the approval request id, so an Executor can read it as its
// Idempotency-Key.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, requestID)
}

// RequestIDFromContext returns the approval request id an Executor should use as its
// Idempotency-Key, or ("", false) if absent.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}

// isKnownActionKind reports whether k is in the closed catalog.
func isKnownActionKind(k string) bool {
	for _, a := range AllActionKinds {
		if a == k {
			return true
		}
	}
	return false
}
