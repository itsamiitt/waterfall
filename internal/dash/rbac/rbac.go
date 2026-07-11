// Package rbac is the dashboard's authorization matrix as data (doc 05 §2) plus the
// server-side ABAC refinement (doc 05 §1.2). It is the human-authorization layer that sits in
// front of every gated write; the deterministic gate (G1..G5) still disposes downstream.
//
// The matrix is DATA, not prose: Can(role, action) is a table lookup, unknown role/action
// fails closed to DecisionDeny, and the same table is served verbatim at GET /v1/admin/roles
// so the SPA guards mirror (never replace) the server authority.
//
// Decision vocabulary maps the matrix cell words: allow -> DecisionAllow, own-tenant-only ->
// DecisionOwnTenant, approval-gated -> DecisionApprovalGated, deny -> DecisionDeny. Cell
// footnotes (operator writes confined to the platform Tenant, MFA step-up, four-eyes, BYO RLS
// scoping) qualify HOW an allowed action executes and are enforced by RLS/MFA/approvals
// elsewhere — they do not change the coarse Decision returned here.
package rbac

// Role constants (doc 05 §1.1) — the closed set bound as app.current_role.
const (
	RoleOperator    = "operator"
	RoleTenantAdmin = "tenant_admin"
	RoleTenantUser  = "tenant_user"
)

// Action is a coarse action group from the doc 05 §2 matrix (the granularity at which RBAC is
// decided; finer routing lives in each feature's handlers).
type Action string

// The P0 action surface. Values are stable dotted identifiers (also the wire form at
// GET /v1/admin/roles).
const (
	OverviewRead     Action = "overview.read"
	ProvidersRead    Action = "providers.read"
	ProvidersWrite   Action = "providers.write"  // create, PATCH, op-state actions
	ProvidersDelete  Action = "providers.delete" // delete, archive (approval-gated)
	KeysRead         Action = "keys.read"
	KeysWrite        Action = "keys.write"  // create, PATCH, enable/disable/rotate/test, single delete
	KeysBulk         Action = "keys.bulk"   // import, bulk op except delete
	KeysDelete       Action = "keys.delete" // bulk delete (approval-gated)
	PoolsWrite       Action = "pools.write"
	RotationWrite    Action = "rotation.write"
	RoutingPublish   Action = "routing.publish"
	WorkflowsPublish Action = "workflows.publish"
	QueuesRead       Action = "queues.read"   // jobs + dead-letter read (own-tenant rows)
	QueuesReplay     Action = "queues.replay" // single redrive, filtered replay
	WorkersRead      Action = "workers.read"
	WorkersActions   Action = "workers.actions"
	CostRead         Action = "cost.read"
	BudgetsWrite     Action = "budgets.write"
	IntentRead       Action = "intent.read" // R&I: computed intent read (Slice 26, ADR-0027)
	AlertsCRUD       Action = "alerts.crud"
	AlertsAck        Action = "alerts.ack"
	UsersCRUD        Action = "users.crud"
	SessionsRevoke   Action = "sessions.revoke"
	AuditRead        Action = "audit.read"
	AuditVerify      Action = "audit.verify"
	ApprovalsDecide  Action = "approvals.decide"
	TenantsProvision Action = "tenants.provision" // operator-only Tenant creation (SEC-3, ADR-0021)
	MFAPolicyWrite   Action = "settings.mfa_policy"
	BulkJobsCancel   Action = "bulk_jobs.cancel"
)

// Decision is the outcome of an RBAC lookup. The zero value is DecisionDeny (fail closed).
type Decision int

const (
	// DecisionDeny: the role may not perform the action (HTTP 403/404).
	DecisionDeny Decision = iota
	// DecisionAllow: permitted (RLS still scopes rows; operator writes still confined to the
	// platform Tenant by WITH CHECK).
	DecisionAllow
	// DecisionOwnTenant: permitted, but only against the caller's own Tenant rows.
	DecisionOwnTenant
	// DecisionApprovalGated: permitted for the role, but executes only through the §9 quorum
	// (the endpoint returns 202 {approval_request_id}).
	DecisionApprovalGated
)

// Allowed reports whether d permits the action at all (any non-deny decision).
func (d Decision) Allowed() bool { return d != DecisionDeny }

// String renders a Decision for the /roles projection and debugging.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionOwnTenant:
		return "own_tenant"
	case DecisionApprovalGated:
		return "approval_gated"
	default:
		return "deny"
	}
}

// matrix encodes doc 05 §2 verbatim (row → per-role Decision). Any (action, role) pair absent
// here resolves to DecisionDeny via Can.
var matrix = map[Action]map[string]Decision{
	OverviewRead: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionOwnTenant},

	ProvidersRead:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionAllow, RoleTenantUser: DecisionAllow}, // TA/TU via tenant_readable projection
	ProvidersWrite:  {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},
	ProvidersDelete: {RoleOperator: DecisionApprovalGated, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},

	KeysRead:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	KeysWrite:  {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	KeysBulk:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	KeysDelete: {RoleOperator: DecisionApprovalGated, RoleTenantAdmin: DecisionApprovalGated, RoleTenantUser: DecisionDeny},
	PoolsWrite: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},

	RotationWrite: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},

	RoutingPublish:   {RoleOperator: DecisionApprovalGated, RoleTenantAdmin: DecisionApprovalGated, RoleTenantUser: DecisionDeny},
	WorkflowsPublish: {RoleOperator: DecisionApprovalGated, RoleTenantAdmin: DecisionApprovalGated, RoleTenantUser: DecisionDeny},

	QueuesRead:   {RoleOperator: DecisionOwnTenant, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	QueuesReplay: {RoleOperator: DecisionOwnTenant, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},

	WorkersRead:    {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},
	WorkersActions: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},

	CostRead:     {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionOwnTenant},
	BudgetsWrite: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	IntentRead:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionOwnTenant},

	AlertsCRUD: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	AlertsAck:  {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},

	UsersCRUD:      {RoleOperator: DecisionOwnTenant, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	SessionsRevoke: {RoleOperator: DecisionOwnTenant, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionAllow}, // TU: own sessions only

	AuditRead:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	AuditVerify: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},

	ApprovalsDecide: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},

	// Provisioning is operator-only; the handler binds the new Tenant per ADR-0021 (DecisionAllow,
	// not OwnTenant — the operator is not acting on its own platform Tenant here). The MFA-policy
	// knob is a tenant self-service setting (own-Tenant); operators may set it cross-Tenant for
	// support (audited). Bulk-job cancel mirrors the create-side ownership (operator any, TA own).
	TenantsProvision: {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionDeny, RoleTenantUser: DecisionDeny},
	MFAPolicyWrite:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
	BulkJobsCancel:   {RoleOperator: DecisionAllow, RoleTenantAdmin: DecisionOwnTenant, RoleTenantUser: DecisionDeny},
}

// Can returns the Decision for role performing action. Unknown role or action fails closed to
// DecisionDeny (doc 05 §2: "Deny is the default").
func Can(role string, a Action) Decision {
	perRole, ok := matrix[a]
	if !ok {
		return DecisionDeny
	}
	return perRole[role] // missing role => zero value => DecisionDeny
}
