package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/security"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Service is the operator Tenant-provisioning + accept-invite service over db.Store. It owns no
// Tenant state: ProvisionTenant binds the NEW Tenant id (ADR-0021) and AcceptInvite binds the
// invite's own Tenant id, both through the dual-GUC tx helper under FORCE RLS.
type Service struct {
	store *db.Store
	audit *audit.Log
	log   *slog.Logger
}

// NewService wires the service to its store and per-Tenant audit chain.
func NewService(store *db.Store, auditLog *audit.Log, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, audit: auditLog, log: logger}
}

// ProvisionTenant creates a customer Tenant, its first tenant_admin User (status 'invited', no
// password), and a one-time hashed invite token — all in ONE transaction that binds
// app.current_tenant to the NEW Tenant id (ADR-0021), so the standard WITH CHECK policies pass for
// the Tenant being created without granting BYPASSRLS. The caller (HTTP RBAC + MFA step-up) has
// already verified the operator's authority; the operator's identity in ctx is used only for the
// audit actor and the invite's created_by. It returns the new Tenant id and the plaintext invite
// token (surfaced to the caller exactly once — only its sha256 is stored).
func (s *Service) ProvisionTenant(ctx context.Context, req ProvisionRequest) (tenantID, inviteToken string, err error) {
	id := strings.TrimSpace(req.ID)
	if !slugRe.MatchString(id) {
		return "", "", ErrInvalidSlug
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return "", "", fmt.Errorf("%w: name is required", ErrValidation)
	}
	email := strings.TrimSpace(req.AdminEmail)
	if !validEmail(email) {
		return "", "", fmt.Errorf("%w: admin_email is invalid", ErrValidation)
	}

	// The operator Principal (verified by the caller) identifies the actor; absence is tolerated
	// (authority was already checked) and simply records a NULL actor/created_by.
	op, _ := tenant.FromContext(ctx)

	token, tokenHash, err := newInviteToken(id)
	if err != nil {
		return "", "", err
	}
	adminID := newUUID()
	inviteID := newUUID()

	// ADR-0021: bind app.current_tenant to the NEW Tenant id (not the operator's 'platform'), so
	// the tenants/users/tenant_invites WITH CHECK (… = app_current_tenant()) policies pass for the
	// Tenant being provisioned. The binding is a single validated id: a bug cannot reach any other
	// existing Tenant's rows.
	provCtx := tenant.WithPrincipal(ctx, tenant.Principal{
		TenantID: id,
		UserID:   op.UserID,
		Scopes:   []string{"role:operator"},
	})

	err = s.store.Tx(provCtx, func(c *pg.Conn) error {
		exists, err := tenantExists(c, id)
		if err != nil {
			return err
		}
		if exists {
			return ErrTenantExists
		}
		if err := insertTenant(c, id, name, req.PlanTier); err != nil {
			return err
		}
		if err := insertAdminUser(c, adminID, id, email); err != nil {
			return err
		}
		if err := insertInvite(c, inviteID, id, email, tokenHash, op.UserID); err != nil {
			return err
		}
		// Audit under the new Tenant, in the SAME transaction (write + audit commit together). No
		// secrets/PII: the plaintext token and admin email are omitted; only ids are recorded.
		after := jsonRaw(map[string]any{
			"tenant_id":     id,
			"name":          name,
			"plan_tier":     req.PlanTier,
			"kind":          tenantKindCustomer,
			"admin_user_id": adminID,
			"invite_id":     inviteID,
		})
		return s.audit.AppendConn(provCtx, c, audit.Entry{
			Action:      "tenant_provisioned",
			ObjectKind:  "tenants",
			ObjectID:    id,
			ActorUserID: op.UserID,
			ActorRole:   "operator",
			After:       after,
		})
	})
	if err != nil {
		return "", "", err
	}
	return id, token, nil
}

// AcceptInvite is the public, token-authenticated (pre-session) path that sets the first admin's
// password. The token is the sole credential: its Tenant prefix is a non-secret routing hint that
// binds app.current_tenant BEFORE RLS discloses the invite row (doc 05 SEC-6), and the invite is
// looked up by sha256(token) under that binding. It verifies the invite is unexpired and unused
// (single-use, enforced atomically by markInviteUsed), sets the User's password_hash
// (security.HashPassword) and status 'active', and audits — all in one Tenant-bound transaction.
func (s *Service) AcceptInvite(ctx context.Context, token, password string) error {
	if len(password) < minPasswordLen {
		return ErrWeakPassword
	}
	tenantID, _, ok := splitToken(token)
	if !ok {
		return ErrInviteNotFound // malformed token: fail closed, existence never disclosed
	}
	hash := hashToken(token)

	// Hash the password OUTSIDE the transaction — PBKDF2 (600k iters) must not hold a tx / row lock
	// open (this is also what keeps the concurrent-accept guard tight).
	pwHash, err := security.HashPassword(password)
	if err != nil {
		return err
	}

	// Bind the tx to the invite's own Tenant id (ADR-0021). Role does not affect the
	// tenant_invites/users isolation policies (they key on tenant only); tenant_admin is bound for
	// semantic accuracy.
	acceptCtx := tenant.WithPrincipal(ctx, tenant.Principal{
		TenantID: tenantID,
		Scopes:   []string{"role:" + roleTenantAdmin},
	})

	return s.store.Tx(acceptCtx, func(c *pg.Conn) error {
		inv, found, err := lookupInvite(c, hash)
		if err != nil {
			return err
		}
		if !found {
			return ErrInviteNotFound
		}
		if inv.Used {
			return ErrInviteUsed
		}
		if inv.Expired {
			return ErrInviteExpired
		}
		// Claim first (single-use guard); a concurrent accept loses the RETURNING race here.
		claimed, err := markInviteUsed(c, inv.ID)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrInviteUsed
		}
		userID, ok, err := setUserPassword(c, inv.Email, pwHash)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInviteNotFound // invited user missing (should not happen); roll back the claim
		}
		after := jsonRaw(map[string]any{"status": userStatusActive, "invite_id": inv.ID})
		return s.audit.AppendConn(acceptCtx, c, audit.Entry{
			Action:      "invite_accepted",
			ObjectKind:  "users",
			ObjectID:    userID,
			ActorUserID: userID,
			ActorRole:   roleTenantAdmin,
			After:       after,
		})
	})
}

// jsonRaw marshals an audit snapshot map; the shapes here cannot fail to marshal (a failure yields
// a nil/empty snapshot rather than aborting the write).
func jsonRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
