package rbac

import (
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

func TestMatrixSpotChecks(t *testing.T) {
	cases := []struct {
		role string
		act  Action
		want Decision
	}{
		// tenant_user is denied user management; operator/tenant_admin are allowed (own-tenant).
		{RoleTenantUser, UsersCRUD, DecisionDeny},
		{RoleOperator, UsersCRUD, DecisionOwnTenant},
		{RoleTenantAdmin, UsersCRUD, DecisionOwnTenant},

		// approval-gated cells.
		{RoleOperator, ProvidersDelete, DecisionApprovalGated},
		{RoleOperator, KeysDelete, DecisionApprovalGated},
		{RoleTenantAdmin, KeysDelete, DecisionApprovalGated},
		{RoleTenantAdmin, RoutingPublish, DecisionApprovalGated},
		{RoleOperator, WorkflowsPublish, DecisionApprovalGated},

		// straightforward allows / denies.
		{RoleOperator, ProvidersWrite, DecisionAllow},
		{RoleTenantAdmin, ProvidersWrite, DecisionDeny},
		{RoleTenantUser, ProvidersRead, DecisionAllow},
		{RoleTenantAdmin, KeysRead, DecisionOwnTenant},
		{RoleTenantUser, KeysRead, DecisionDeny},
		{RoleOperator, WorkersActions, DecisionAllow},
		{RoleTenantAdmin, WorkersActions, DecisionDeny},
		{RoleTenantUser, OverviewRead, DecisionOwnTenant},
		{RoleTenantUser, CostRead, DecisionOwnTenant},
		{RoleTenantUser, SessionsRevoke, DecisionAllow},
		{RoleTenantAdmin, AuditVerify, DecisionOwnTenant},
		{RoleTenantUser, AuditRead, DecisionDeny},
	}
	for _, c := range cases {
		if got := Can(c.role, c.act); got != c.want {
			t.Errorf("Can(%q, %q) = %v, want %v", c.role, c.act, got, c.want)
		}
	}
}

func TestUnknownFailsClosed(t *testing.T) {
	if got := Can("superadmin", UsersCRUD); got != DecisionDeny {
		t.Errorf("unknown role = %v, want DecisionDeny", got)
	}
	if got := Can(RoleOperator, Action("nonexistent.action")); got != DecisionDeny {
		t.Errorf("unknown action = %v, want DecisionDeny", got)
	}
	if got := Can("", ""); got != DecisionDeny {
		t.Errorf("empty inputs = %v, want DecisionDeny", got)
	}
}

func TestDecisionHelpers(t *testing.T) {
	if DecisionDeny.Allowed() {
		t.Error("DecisionDeny.Allowed() should be false")
	}
	for _, d := range []Decision{DecisionAllow, DecisionOwnTenant, DecisionApprovalGated} {
		if !d.Allowed() {
			t.Errorf("%v.Allowed() should be true", d)
		}
	}
	if DecisionApprovalGated.String() != "approval_gated" {
		t.Errorf("String() = %q", DecisionApprovalGated.String())
	}
}

func TestCheckABAC(t *testing.T) {
	p := tenant.Principal{
		TenantID: "acme",
		Scopes:   []string{"role:operator", "region:eu", "plan_tier:pro"},
	}
	cases := []struct {
		name string
		req  ABAC
		want bool
	}{
		{"no constraints", ABAC{}, true},
		{"matching region", ABAC{Region: "eu"}, true},
		{"wrong region", ABAC{Region: "us"}, false},
		{"matching plan", ABAC{PlanTier: "pro"}, true},
		{"wrong plan", ABAC{PlanTier: "enterprise"}, false},
		{"both match", ABAC{Region: "eu", PlanTier: "pro"}, true},
		{"one fails", ABAC{Region: "eu", PlanTier: "enterprise"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CheckABAC(p, c.req); got != c.want {
				t.Errorf("CheckABAC(%+v) = %v, want %v", c.req, got, c.want)
			}
		})
	}
}
