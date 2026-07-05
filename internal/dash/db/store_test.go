package db

import (
	"testing"

	"github.com/enrichment/waterfall/internal/tenant"
)

func TestRoleFromPrincipal(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		want   string
	}{
		{"operator", []string{"admin:read", "role:operator"}, "operator"},
		{"tenant_admin", []string{"role:tenant_admin", "admin:write"}, "tenant_admin"},
		{"tenant_user", []string{"role:tenant_user"}, "tenant_user"},
		{"none", []string{"admin:read", "admin:write"}, ""},
		{"empty", nil, ""},
		{"unknown role scope ignored", []string{"role:superadmin"}, ""},
		{"first recognized wins", []string{"role:tenant_user", "role:operator"}, "tenant_user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := tenant.Principal{TenantID: "acme", Scopes: c.scopes}
			if got := RoleFromPrincipal(p); got != c.want {
				t.Errorf("RoleFromPrincipal(%v) = %q, want %q", c.scopes, got, c.want)
			}
		})
	}
}
