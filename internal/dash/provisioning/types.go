// Package provisioning is the operator Tenant-provisioning path (SEC-3, ADR-0021): the audited
// endpoint that creates a customer Tenant, its first tenant_admin User (status 'invited', no
// password), and a one-time hashed invite token — plus the public, token-authenticated
// accept-invite path that sets the first admin's password.
//
// The RLS trick (ADR-0021): the operator's authority (operator role + MFA step-up) is checked in
// the HTTP layer; the DB transaction then binds app.current_tenant to the NEW Tenant's id (not
// 'platform'), so the standard WITH CHECK (… = app_current_tenant()) policies on tenants/users/
// tenant_invites pass for the Tenant being created — no BYPASSRLS, and the binding is a single
// validated id so a bug can never touch a different existing Tenant's rows.
//
// The invite token is structured "<tenant_id>|<256-bit-secret>" (base64url), mirroring the
// session-cookie routing-hint deviation (doc 05 SEC-6): the tenant prefix is a non-secret hint
// that lets the pre-session accept-invite path bind app.current_tenant BEFORE RLS will disclose
// the invite row; the 256-bit random secret remains the sole authenticator, and only its sha256
// is ever stored (never the plaintext). A tampered prefix fails closed (RLS returns zero rows).
package provisioning

import (
	"errors"
	"regexp"
)

const (
	// slugPattern mirrors the tenants.id CHECK constraint (migration 0004).
	slugPattern = `^[a-z0-9-]{1,64}$`

	// inviteRandomBytes is the entropy of the invite secret (256-bit, doc 15 §T1 / ADR-0021).
	inviteRandomBytes = 32

	// minPasswordLen is the floor for a first-admin password set via accept-invite.
	minPasswordLen = 8

	roleTenantAdmin    = "tenant_admin"
	tenantKindCustomer = "customer"
	tenantStatusActive = "active"
	userStatusInvited  = "invited"
	userStatusActive   = "active"
)

// slugRe is compiled once; it validates the requested Tenant id against the slug CHECK.
var slugRe = regexp.MustCompile(slugPattern)

// ProvisionRequest is the operator's create-Tenant input (doc 15 §T1). All fields are validated
// server-side; the role and tenant binding are never read from this struct.
type ProvisionRequest struct {
	ID         string // Tenant slug (validated against slugPattern, must not already exist)
	Name       string // display name (required)
	PlanTier   string // optional plan tier ("" => NULL)
	AdminEmail string // first tenant_admin email (required)
}

// invite is the read model of a tenant_invites row for the accept path. Used/Expired are computed
// in SQL so the accept path never parses timestamptz text.
type invite struct {
	ID      string
	Email   string
	Role    string
	Used    bool
	Expired bool
}

// Sentinel errors. None discloses cross-Tenant existence or carries secret material; the HTTP
// layer maps each to a uniform error body (doc 04 §1.6).
var (
	// ErrInvalidSlug: the requested Tenant id fails the slug CHECK (^[a-z0-9-]{1,64}$).
	ErrInvalidSlug = errors.New("provisioning: tenant id must match ^[a-z0-9-]{1,64}$")
	// ErrValidation: a well-formed body that is semantically invalid (missing name / bad email).
	ErrValidation = errors.New("provisioning: validation failed")
	// ErrTenantExists: a Tenant with the requested id already exists.
	ErrTenantExists = errors.New("provisioning: tenant already exists")
	// ErrWeakPassword: the accept-invite password is shorter than minPasswordLen.
	ErrWeakPassword = errors.New("provisioning: password too short")
	// ErrInviteNotFound: no matching, still-valid invite (also returned for a malformed token).
	ErrInviteNotFound = errors.New("provisioning: invite not found")
	// ErrInviteExpired: the invite's expires_at has passed.
	ErrInviteExpired = errors.New("provisioning: invite expired")
	// ErrInviteUsed: the invite was already accepted (single-use).
	ErrInviteUsed = errors.New("provisioning: invite already used")
)
