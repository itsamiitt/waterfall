package approvals

import (
	"context"

	"github.com/enrichment/waterfall/internal/pg"
)

// defaultPolicy is the built-in FAIL-CLOSED default (doc 05 §9.1): required_approvals=1,
// expires_after_s=86400, approver_role='tenant_admin' — 'operator' for the platform action_kinds
// (provider_delete / provider_archive / secrets_backend_change). It gates every action_kind even
// when no approval_policies row exists, and covers action_kinds added later automatically.
func defaultPolicy(actionKind string) Policy {
	role := "tenant_admin"
	if platformActionKinds[actionKind] {
		role = "operator"
	}
	return Policy{
		ActionKind:        actionKind,
		RequiredApprovals: 1,
		ApproverRole:      role,
		ExpiresAfterS:     86400,
		Default:           true,
	}
}

// resolvePolicy returns the tenant's approval_policies row for actionKind if present, else the
// built-in default. An explicit row only CUSTOMIZES the knobs — it can never disarm the gate:
// required_approvals is clamped to a minimum of 1 (doc 05 §9.1: "required_approvals >= 1 always"),
// so no writable row state weakens four-eyes+quorum. The read runs under the caller's Principal
// (RLS-scoped), so a tenant sees only its own policy.
func (s *Service) resolvePolicy(ctx context.Context, actionKind string) (Policy, error) {
	pol := defaultPolicy(actionKind)
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select required_approvals, approver_role, expires_after_s
			   from approval_policies where action_kind = $1`, actionKind)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return nil // no row => keep the fail-closed default
		}
		row := res.Rows[0]
		pol = Policy{ActionKind: actionKind, Default: false}
		pol.RequiredApprovals = atoiOr(strOf(row[0]), 1)
		pol.ApproverRole = strOf(row[1])
		pol.ExpiresAfterS = atoiOr(strOf(row[2]), 86400)
		// Fail-closed clamps: an explicit row customizes but never disarms.
		if pol.RequiredApprovals < 1 {
			pol.RequiredApprovals = 1
		}
		if pol.ApproverRole == "" {
			pol.ApproverRole = defaultPolicy(actionKind).ApproverRole
		}
		if pol.ExpiresAfterS < 1 {
			pol.ExpiresAfterS = 86400
		}
		return nil
	})
	if err != nil {
		return Policy{}, err
	}
	return pol, nil
}
