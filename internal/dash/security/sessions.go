package security

import (
	"context"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Session lifetimes (doc 05 §4.1): 30-minute sliding idle, 12-hour absolute, and a 24h forensic
// grace before the reaper deletes an expired/revoked row.
const (
	idleTTL       = 30 * time.Minute
	absoluteTTL   = 12 * time.Hour
	slideInterval = time.Minute // bound last_seen_at write load
	reapGrace     = 24 * time.Hour
)

// Session is a resolved, still-valid browser session joined to its user's role and status.
type Session struct {
	ID                string
	TenantID          string
	UserID            string
	Role              string
	CSRFToken         string
	MFAVerified       bool
	MFARequired       bool
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

// Sessions creates, resolves, revokes, lists, and reaps browser sessions. The cookie value it
// mints is "<tenant_id>|<session_id>": the tenant is a non-secret routing hint that lets the
// resolver bind app.current_tenant before RLS will disclose the row, keeping the app role
// non-BYPASSRLS (G1). The 256-bit id remains the sole authenticator — tampering with the tenant
// prefix fails closed (RLS returns zero rows). Deviation D-P0-1 (doc 05 §4.1).
type Sessions struct {
	store *db.Store
	now   func() time.Time
}

// NewSessions builds a session service over store using the wall clock.
func NewSessions(store *db.Store) *Sessions { return &Sessions{store: store, now: time.Now} }

// WithClock injects a clock (tests).
func (s *Sessions) WithClock(now func() time.Time) *Sessions { s.now = now; return s }

// Create inserts a session for userID under the caller's Principal tenant and returns the cookie
// value and CSRF token. mfaVerified stamps mfa_verified_at (set on the post-MFA rotated session).
func (s *Sessions) Create(ctx context.Context, userID, ip, ua string, mfaVerified bool) (cookie, csrf string, err error) {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return "", "", err
	}
	id := randID(32)  // 256-bit
	csrf = randID(16) // 128-bit
	now := s.now().UTC()
	var mfaAt any
	if mfaVerified {
		mfaAt = now
	}
	err = s.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into sessions
			   (id, tenant_id, user_id, csrf_token, ip, user_agent, idle_expires_at, absolute_expires_at, mfa_verified_at)
			 values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, p.TenantID, userID, csrf, nullIf(ip), nullIf(ua),
			now.Add(idleTTL), now.Add(absoluteTTL), mfaAt)
	})
	if err != nil {
		return "", "", err
	}
	return p.TenantID + "|" + id, csrf, nil
}

// Resolve validates a cookie value into a Session: it parses the tenant hint, binds the tenant,
// selects the un-revoked, un-expired row joined to its active user, and slides the idle window at
// most once per minute. Any failure returns ErrSession (never distinguished on the wire).
func (s *Sessions) Resolve(ctx context.Context, cookie string) (Session, error) {
	tenantID, id, ok := splitCookie(cookie)
	if !ok {
		return Session{}, ErrSession
	}
	rctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tenantID})
	now := s.now().UTC()
	var sess Session
	found := false
	err := s.store.Tx(rctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select s.tenant_id, s.user_id, s.csrf_token, s.mfa_verified_at, s.idle_expires_at,
			        s.absolute_expires_at, s.revoked_at, s.last_seen_at, u.role, u.status, u.mfa_enrolled_at,
			        t.require_mfa
			 from sessions s join users u on u.id = s.user_id join tenants t on t.id = s.tenant_id
			 where s.id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		if row[6] != nil { // revoked_at
			return nil
		}
		idleExp := parseTS(str(row[4]))
		absExp := parseTS(str(row[5]))
		if now.After(idleExp) || now.After(absExp) {
			return nil
		}
		if str(row[9]) != "active" { // user status
			return nil
		}
		sess = Session{
			ID:                id,
			TenantID:          str(row[0]),
			UserID:            str(row[1]),
			CSRFToken:         str(row[2]),
			Role:              str(row[8]),
			MFAVerified:       row[3] != nil,
			IdleExpiresAt:     idleExp,
			AbsoluteExpiresAt: absExp,
		}
		// MFA is required for this session when the role mandates it (operator/tenant_admin), when the
		// user has enrolled a TOTP seed (mfa_enrolled_at set), OR when the Tenant's require_mfa knob is
		// on (SEC-5) — the last case gates an unenrolled tenant_user in a require-MFA Tenant until they
		// enroll+verify, so mfaOK stays false and every route but the MFA-exempt enrollment endpoints
		// returns 401 mfa_required.
		sess.MFARequired = RequiresMFA(sess.Role) || row[10] != nil || boolText(row[11])
		found = true
		// Slide the idle window, throttled to once per minute.
		if now.Sub(parseTS(str(row[7]))) > slideInterval {
			_ = c.ExecParams(
				`update sessions set last_seen_at = $2, idle_expires_at = $3 where id = $1`,
				id, now, now.Add(idleTTL))
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	if !found {
		return Session{}, ErrSession
	}
	return sess, nil
}

// Revoke marks a session revoked in the caller's tenant scope. Revoking an absent/other-tenant id
// is a no-op (RLS scopes the UPDATE); the boolean reports whether a row changed.
func (s *Sessions) Revoke(ctx context.Context, id string) (bool, error) {
	changed := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update sessions set revoked_at = now() where id = $1 and revoked_at is null returning id`, id)
		if err != nil {
			return err
		}
		changed = len(res.Rows) > 0
		return nil
	})
	return changed, err
}

// RevokeAllForUser revokes every live (un-revoked) session for userID in the caller's tenant scope
// in a single statement and returns the number of sessions actually revoked. It is the bulk form of
// Revoke (RB-14 step 7 session-hygiene, OI-RB-2): RLS scopes the UPDATE to the caller's bound
// tenant, so a userID in another tenant is a no-op (0) — a compromised or restored account's whole
// session set is cut in one round-trip instead of an N-call per-id DELETE loop. It is
// audited-capable: the caller records one audit row carrying the returned count. Idempotent — a
// second call after all sessions are already revoked returns 0.
func (s *Sessions) RevokeAllForUser(ctx context.Context, userID string) (int, error) {
	n := 0
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		var e error
		n, e = revokeAllSessionsForUser(c, userID)
		return e
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// revokeAllSessionsForUser marks every live session for userID revoked on the supplied, already
// tenant-bound conn and returns how many rows changed. It runs on a caller-provided conn (not its
// own Tx) so it composes into a larger transaction — the user-deactivate and password-reset paths
// revoke sessions in the SAME transaction as their user write (doc 04 §2.2), while
// Sessions.RevokeAllForUser wraps it in a standalone Tx. RLS scopes the UPDATE to the bound tenant.
func revokeAllSessionsForUser(c *pg.Conn, userID string) (int, error) {
	res, err := c.QueryParams(
		`update sessions set revoked_at = now() where user_id = $1 and revoked_at is null returning id`, userID)
	if err != nil {
		return 0, err
	}
	return len(res.Rows), nil
}

// SessionInfo is a listed session (never includes the CSRF token or full id secret material).
type SessionInfo struct {
	ID                string
	UserID            string
	CreatedAt         time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	MFAVerified       bool
}

// List returns active sessions in the caller's tenant scope: own-user only unless all is set
// (tenant_admin+ sees the whole tenant, doc 04 §2.1).
func (s *Sessions) List(ctx context.Context, userID string, all bool) ([]SessionInfo, error) {
	var out []SessionInfo
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		q := `select id, user_id, created_at, idle_expires_at, absolute_expires_at, mfa_verified_at
		      from sessions where revoked_at is null`
		var res *pg.Result
		var err error
		if all {
			res, err = c.QueryParams(q + ` order by created_at desc`)
		} else {
			res, err = c.QueryParams(q+` and user_id = $1 order by created_at desc`, userID)
		}
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, SessionInfo{
				ID: str(row[0]), UserID: str(row[1]), CreatedAt: parseTS(str(row[2])),
				IdleExpiresAt: parseTS(str(row[3])), AbsoluteExpiresAt: parseTS(str(row[4])),
				MFAVerified: row[5] != nil,
			})
		}
		return nil
	})
	return out, err
}

// DeleteExpired is the reaper (doc 05 §4.1): it deletes rows 24h past expiry or revocation. It
// enumerates tenants via the operator SELECT policy on tenants, then deletes per-tenant under
// each tenant's own binding — no BYPASSRLS. Expiry is enforced at authentication time regardless,
// so a lagging reaper never extends a session.
func (s *Sessions) DeleteExpired(ctx context.Context) error {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "platform", Scopes: []string{"role:operator"}})
	var tenants []string
	if err := s.store.Tx(sysCtx, func(c *pg.Conn) error {
		res, err := c.Query(`select id from tenants`)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			tenants = append(tenants, str(row[0]))
		}
		return nil
	}); err != nil {
		return err
	}
	cutoff := s.now().UTC().Add(-reapGrace)
	for _, tid := range tenants {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		_ = s.store.Tx(tctx, func(c *pg.Conn) error {
			return c.ExecParams(
				`delete from sessions
				 where greatest(idle_expires_at, absolute_expires_at) < $1
				    or (revoked_at is not null and revoked_at < $1)`, cutoff)
		})
	}
	return nil
}

func splitCookie(v string) (tenantID, id string, ok bool) {
	i := strings.IndexByte(v, '|')
	if i <= 0 || i == len(v)-1 {
		return "", "", false
	}
	return v[:i], v[i+1:], true
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}
