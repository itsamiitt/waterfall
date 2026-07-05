package security

import (
	"context"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// User is the read model of a users row (never carries the password hash off the auth path).
type User struct {
	ID            string
	TenantID      string
	Email         string
	Role          string
	Status        string
	MFAEnvelopeID string // "" when no TOTP seed is sealed
	MFAEnrolled   bool
}

// Users is the tenant-scoped user service over db.Store. Cross-tenant reads happen ONLY on the
// login lookup path via the enumerated users_operator_read policy (doc 05 §3.3), never for a
// request-driven handler.
type Users struct {
	store   *db.Store
	secrets secrets.Backend
	issuer  string
}

// NewUsers wires a Users service to its store, the secrets backend (for TOTP seed seal/open), and
// the OTP issuer label shown in authenticator apps.
func NewUsers(store *db.Store, backend secrets.Backend, issuer string) *Users {
	if issuer == "" {
		issuer = "Waterfall"
	}
	return &Users{store: store, secrets: backend, issuer: issuer}
}

// Create inserts a user under the caller's Principal tenant (tenant_id and RLS WITH CHECK both
// derive from the verified Principal). passwordHash must already be a pbkdf2-sha256 encoding.
func (u *Users) Create(ctx context.Context, email, passwordHash, role string) (string, error) {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return "", err
	}
	id := newUUID()
	err = u.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into users (id, tenant_id, email, password_hash, role) values ($1,$2,$3,$4,$5)`,
			id, p.TenantID, email, passwordHash, role)
	})
	return id, err
}

// AuthLookup resolves a login by email across every tenant, using a server-internal operator
// Principal so the enumerated users_operator_read SELECT policy applies (doc 05 §3.3). It is the
// pre-authentication path; the login itself is what gets audited. Returns the user, its password
// hash, and whether it was found.
func (u *Users) AuthLookup(ctx context.Context, email string) (User, string, bool, error) {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{
		TenantID: "platform",
		Scopes:   []string{"role:operator"},
	})
	var usr User
	var pwHash string
	found := false
	err := u.store.Tx(sysCtx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id, tenant_id, email, password_hash, role, status, mfa_totp_envelope_id, mfa_enrolled_at
			 from users where lower(email) = lower($1) limit 1`, email)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		usr = User{
			ID:            str(row[0]),
			TenantID:      str(row[1]),
			Email:         str(row[2]),
			Role:          str(row[4]),
			Status:        str(row[5]),
			MFAEnvelopeID: str(row[6]),
			MFAEnrolled:   row[7] != nil,
		}
		pwHash = str(row[3])
		found = true
		return nil
	})
	return usr, pwHash, found, err
}

// GetByID reads one user in the caller's tenant scope (RLS enforced). Absent => ErrNotFound.
func (u *Users) GetByID(ctx context.Context, id string) (User, error) {
	var usr User
	found := false
	err := u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id, tenant_id, email, role, status, mfa_totp_envelope_id, mfa_enrolled_at
			 from users where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		usr = User{
			ID:            str(row[0]),
			TenantID:      str(row[1]),
			Email:         str(row[2]),
			Role:          str(row[3]),
			Status:        str(row[4]),
			MFAEnvelopeID: str(row[5]),
			MFAEnrolled:   row[6] != nil,
		}
		found = true
		return nil
	})
	if err != nil {
		return User{}, err
	}
	if !found {
		return User{}, ErrNotFound
	}
	return usr, nil
}

// List returns users in the caller's tenant scope, keyset-paginated by (created_at, id) DESC and
// bounded by db.ClampLimit. Operators reading cross-tenant is an enumerated, audited path handled
// by the caller binding an operator Principal.
func (u *Users) List(ctx context.Context, cur db.Cursor, limit int) ([]User, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var out []User
	var next db.Cursor
	err := u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id, tenant_id, email, role, status, mfa_totp_envelope_id, mfa_enrolled_at
			 from users order by created_at desc, id desc limit $1`, int64(limit+1))
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			last := rows[limit-1]
			next = db.Cursor{ID: str(last[0])}
			rows = rows[:limit]
		}
		for _, row := range rows {
			out = append(out, User{
				ID: str(row[0]), TenantID: str(row[1]), Email: str(row[2]),
				Role: str(row[3]), Status: str(row[4]),
				MFAEnvelopeID: str(row[5]), MFAEnrolled: row[6] != nil,
			})
		}
		return nil
	})
	return out, next, err
}

// Deactivate soft-deletes a user (status='deactivated') and revokes its sessions in the SAME
// transaction (doc 04 §2.2). Returns ErrNotFound when no such user exists in the tenant scope.
func (u *Users) Deactivate(ctx context.Context, id string) error {
	return u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update users set status = 'deactivated', updated_at = now() where id = $1 returning id`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return c.ExecParams(
			`update sessions set revoked_at = now() where user_id = $1 and revoked_at is null`, id)
	})
}

// UpdateRoleStatus applies a partial update to role and/or status ("" leaves a field unchanged).
func (u *Users) UpdateRoleStatus(ctx context.Context, id, role, status string) error {
	return u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update users
			   set role = coalesce(nullif($2,''), role),
			       status = coalesce(nullif($3,''), status),
			       updated_at = now()
			 where id = $1 returning id`, id, role, status)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ResetPassword sets a new password hash and revokes all the user's sessions in one transaction.
func (u *Users) ResetPassword(ctx context.Context, id, passwordHash string) error {
	return u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update users set password_hash = $2, updated_at = now() where id = $1 returning id`, id, passwordHash)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return ErrNotFound
		}
		return c.ExecParams(
			`update sessions set revoked_at = now() where user_id = $1 and revoked_at is null`, id)
	})
}

// EnrollMFA generates a fresh TOTP seed, seals it via the secrets backend (kind='totp_seed'),
// records the envelope id on the user, and returns the plaintext seed plus the otpauth:// URI
// (both surfaced to the client exactly once). The plaintext seed never persists.
func (u *Users) EnrollMFA(ctx context.Context, userID, email string) (seed []byte, otpauthURL string, err error) {
	seed = NewSeed()
	envID, err := u.secrets.Seal(ctx, "totp_seed", seed)
	if err != nil {
		return nil, "", err
	}
	err = u.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`update users set mfa_totp_envelope_id = $1, updated_at = now() where id = $2`,
			string(envID), userID)
	})
	if err != nil {
		return nil, "", err
	}
	return seed, OTPAuthURL(u.issuer, email, seed), nil
}

// TOTPSeed opens the user's sealed TOTP seed via the secrets backend. ErrMFANotEnrolled when the
// user has no envelope id.
func (u *Users) TOTPSeed(ctx context.Context, userID string) ([]byte, error) {
	usr, err := u.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if usr.MFAEnvelopeID == "" {
		return nil, ErrMFANotEnrolled
	}
	return u.secrets.Open(ctx, secrets.EnvelopeID(usr.MFAEnvelopeID))
}

// ConfirmMFA verifies the first TOTP code against the sealed seed, marks the user enrolled, and
// issues fresh single-use recovery codes (replacing any prior set) in one transaction. It returns
// the plaintext recovery codes exactly once. A code that does not verify returns ErrBadCode.
func (u *Users) ConfirmMFA(ctx context.Context, userID, code string, at time.Time) (codes []string, err error) {
	seed, err := u.TOTPSeed(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !VerifyTOTP(seed, code, at) {
		return nil, ErrBadCode
	}
	plain, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	err = u.store.Tx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(`update users set mfa_enrolled_at = now(), updated_at = now() where id = $1`, userID); err != nil {
			return err
		}
		if err := c.ExecParams(`delete from mfa_recovery_codes where user_id = $1`, userID); err != nil {
			return err
		}
		for _, h := range hashes {
			if err := c.ExecParams(
				`insert into mfa_recovery_codes (tenant_id, user_id, code_hash) values ($1,$2,$3::bytea)`,
				p.TenantID, userID, encodeBytea(h)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// ConsumeRecoveryCode marks a matching unused recovery code used (single-use) in one transaction
// and reports whether a code was consumed (doc 05 §5.2).
func (u *Users) ConsumeRecoveryCode(ctx context.Context, userID, code string) (bool, error) {
	consumed := false
	err := u.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update mfa_recovery_codes set used_at = now()
			 where user_id = $1 and code_hash = $2::bytea and used_at is null returning user_id`,
			userID, encodeBytea(HashRecoveryCode(code)))
		if err != nil {
			return err
		}
		consumed = len(res.Rows) > 0
		return nil
	})
	return consumed, err
}
