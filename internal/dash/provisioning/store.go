package provisioning

import "github.com/enrichment/waterfall/internal/pg"

// This file holds the SQL persistence helpers for the provisioning service. Every helper operates
// on an ALREADY-OPEN *pg.Conn whose dual-GUC binding (app.current_tenant / app.current_role) was
// established by the caller's db.Store transaction — so RLS (FORCE ROW LEVEL SECURITY) is in force
// on every statement. Provisioning binds the NEW Tenant id (ADR-0021); accept-invite binds the
// invite's own Tenant id. No helper reads tenant_id from an argument for authority.

// tenantExists reports whether a Tenant row with id is visible under the current binding. During
// provisioning the binding is the new id, so the isolation policy (id = app_current_tenant())
// discloses exactly the row being checked for.
func tenantExists(c *pg.Conn, id string) (bool, error) {
	res, err := c.QueryParams(`select 1 from tenants where id = $1`, id)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// insertTenant creates the customer Tenant row (status 'active'). planTier "" is stored as NULL.
func insertTenant(c *pg.Conn, id, name, planTier string) error {
	return c.ExecParams(
		`insert into tenants (id, name, kind, plan_tier, status) values ($1,$2,$3,$4,$5)`,
		id, name, tenantKindCustomer, nullIf(planTier), tenantStatusActive)
}

// insertAdminUser creates the first tenant_admin User: status 'invited', empty password_hash
// (the real hash is set through accept-invite). tenant_id equals the bound Tenant, so the users
// WITH CHECK policy passes.
func insertAdminUser(c *pg.Conn, id, tenantID, email string) error {
	return c.ExecParams(
		`insert into users (id, tenant_id, email, password_hash, role, status)
		 values ($1,$2,$3,'',$4,$5)`,
		id, tenantID, email, roleTenantAdmin, userStatusInvited)
}

// insertInvite writes the one-time invite: role tenant_admin, the sha256 token hash, and a fixed
// 72h expiry computed by the database. created_by is the provisioning operator's User id ("" =>
// NULL).
func insertInvite(c *pg.Conn, id, tenantID, email string, tokenHash []byte, createdBy string) error {
	return c.ExecParams(
		`insert into tenant_invites (id, tenant_id, email, role, token_hash, expires_at, created_by)
		 values ($1,$2,$3,$4,$5::bytea, now() + interval '72 hours', $6)`,
		id, tenantID, email, roleTenantAdmin, encodeBytea(tokenHash), nullIf(createdBy))
}

// lookupInvite fetches the invite matching tokenHash under the current Tenant binding, computing
// used/expired in SQL (so the caller never parses timestamptz). found=false when RLS discloses no
// row — including a token whose Tenant prefix did not match the binding (fail closed).
func lookupInvite(c *pg.Conn, tokenHash []byte) (invite, bool, error) {
	res, err := c.QueryParams(
		`select id, email, role, (used_at is not null), (expires_at <= now())
		 from tenant_invites where token_hash = $1::bytea`, encodeBytea(tokenHash))
	if err != nil {
		return invite{}, false, err
	}
	if len(res.Rows) == 0 {
		return invite{}, false, nil
	}
	row := res.Rows[0]
	return invite{
		ID:      str(row[0]),
		Email:   str(row[1]),
		Role:    str(row[2]),
		Used:    str(row[3]) == "t",
		Expired: str(row[4]) == "t",
	}, true, nil
}

// markInviteUsed atomically claims the invite: the UPDATE ... WHERE used_at IS NULL RETURNING is
// the single-use guard. A concurrent accept blocks on the row lock and, once the winner commits,
// re-evaluates the predicate to zero rows — so claimed=false means "already used" (race-safe).
func markInviteUsed(c *pg.Conn, id string) (bool, error) {
	res, err := c.QueryParams(
		`update tenant_invites set used_at = now() where id = $1 and used_at is null returning id`, id)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// setUserPassword sets the first admin's password hash and flips status 'invited' -> 'active',
// returning the User id. ok=false when no invited User with that email exists under the binding.
func setUserPassword(c *pg.Conn, email, passwordHash string) (userID string, ok bool, err error) {
	res, err := c.QueryParams(
		`update users set password_hash = $2, status = $3, updated_at = now()
		 where lower(email) = lower($1) and status = $4 returning id`,
		email, passwordHash, userStatusActive, userStatusInvited)
	if err != nil {
		return "", false, err
	}
	if len(res.Rows) == 0 {
		return "", false, nil
	}
	return str(res.Rows[0][0]), true, nil
}

// --- small column helpers (kept local so the package stays self-contained) ---

func str(p *string) string {
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
