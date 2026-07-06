package security

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// TenantPolicy reads and writes per-Tenant security knobs on the tenants row (doc 05 §5.3, SEC-5).
// It is the read/write seam for the require_mfa flag (migration 0012): a tenant_admin may require
// MFA for every User of their Tenant (default off). All access runs through the dual-GUC RLS
// transaction helper, so the tenants_tenant_isolation policy (USING/​WITH CHECK id =
// app_current_tenant()) confines a read or write to the caller's own Tenant — an operator Principal,
// bound to 'platform', can therefore only read/write the platform row (there is deliberately no
// operator cross-Tenant write policy on tenants; doc 05 §3.3 footnote 6).
type TenantPolicy struct {
	store *db.Store
}

// NewTenantPolicy wires a TenantPolicy over the shared store.
func NewTenantPolicy(store *db.Store) *TenantPolicy { return &TenantPolicy{store: store} }

// RequireMFA reports whether tenantID requires MFA for all its Users (tenants.require_mfa, SEC-5).
// It reads under the ctx Principal's dual-GUC binding, so the caller must be bound to tenantID (the
// login path binds the user's own Tenant before calling this). An absent/invisible row yields false
// (fail-open to the documented default — the knob is off unless explicitly set); only a storage
// fault returns a non-nil error.
func (t *TenantPolicy) RequireMFA(ctx context.Context, tenantID string) (bool, error) {
	required := false
	err := t.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(`select require_mfa from tenants where id = $1`, tenantID)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return nil
		}
		required = boolText(res.Rows[0][0])
		return nil
	})
	return required, err
}

// SetRequireMFA sets tenants.require_mfa for tenantID (SEC-5). The UPDATE is doubly scoped: the
// caller passes their own Principal's Tenant id, and the tenants_tenant_isolation RLS policy
// (WITH CHECK id = app_current_tenant()) rejects any write to another Tenant's row — so a
// tenant_admin can only toggle their own Tenant. A tenantID not visible/writable in the caller's
// scope affects zero rows and returns ErrNotFound. Auditing is the handler's responsibility (this
// method is audited-capable: the mfa-policy handler appends one audit row for the change).
func (t *TenantPolicy) SetRequireMFA(ctx context.Context, tenantID string, require bool) error {
	return t.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update tenants set require_mfa = $2 where id = $1 returning id`, tenantID, require)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// VerifyStepUp verifies a step-up proof that may be EITHER a fresh TOTP code OR a single-use
// recovery code (doc 05 §5.2/§5.4, T5e). It is the one reusable verifier the step-up call sites
// (keys create/import/rotate/bulk, approvals decide, the mfa-policy knob) share: it first tries
// VerifyAndConsume — which records the (user, time_step) single-use marker so a captured TOTP cannot
// be replayed inside its ±1-step window (OI-SEC-8) — and, only on a miss, falls back to
// ConsumeRecoveryCode, which single-uses a recovery code transactionally. It returns ok=true when
// either proof succeeds (each strictly single-use), (false, nil) when neither matches, and a
// non-nil error only on a storage/crypto fault on the recovery path. tenant_id is taken from the ctx
// Principal (G1), never an argument.
func (u *Users) VerifyStepUp(ctx context.Context, userID, code string, now time.Time) (bool, error) {
	if ok, err := u.VerifyAndConsume(ctx, userID, code, now); err == nil && ok {
		return true, nil
	}
	// A TOTP miss (wrong/expired/replayed code, or the seed could not be opened) falls through to the
	// recovery-code path; a recovery code is never a valid 6-digit TOTP, so no double-consumption is
	// possible. The consuming UPDATE is idempotent-safe: a second presentation of the same code finds
	// used_at already set and returns false.
	return u.ConsumeRecoveryCode(ctx, userID, code)
}

// boolText decodes a Postgres boolean rendered in the internal/pg text protocol ("t"/"f"), tolerating
// the spelled-out "true" form. NULL (nil) decodes to false.
func boolText(p *string) bool {
	return p != nil && (*p == "t" || *p == "true")
}
