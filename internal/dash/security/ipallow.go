package security

import (
	"context"
	"net"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// IPRule is one CIDR entry in a tenant's allowlist.
type IPRule struct {
	CIDR  string
	Label string
}

// IPAllow manages the per-tenant CIDR allowlist and answers the enforcement question (doc 05 §6).
type IPAllow struct {
	store *db.Store
}

// NewIPAllow builds the service over store.
func NewIPAllow(store *db.Store) *IPAllow { return &IPAllow{store: store} }

// List returns the caller tenant's allowlist rules.
func (a *IPAllow) List(ctx context.Context) ([]IPRule, error) {
	var out []IPRule
	err := a.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.Query(`select cidr, label from ip_allowlists order by created_at asc`)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, IPRule{CIDR: str(row[0]), Label: str(row[1])})
		}
		return nil
	})
	return out, err
}

// Replace does a full-replacement PUT of the tenant's allowlist in one transaction (doc 04 §2.2).
// Every CIDR must parse; createdBy is the acting user id ("" allowed).
func (a *IPAllow) Replace(ctx context.Context, rules []IPRule, createdBy string) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if _, _, e := net.ParseCIDR(r.CIDR); e != nil {
			return e
		}
	}
	return a.store.Tx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(`delete from ip_allowlists`); err != nil {
			return err
		}
		for _, r := range rules {
			if err := c.ExecParams(
				`insert into ip_allowlists (id, tenant_id, cidr, label, created_by)
				 values ($1,$2,$3,$4,$5)`,
				newUUID(), p.TenantID, r.CIDR, nullIf(r.Label), nullIf(createdBy)); err != nil {
				return err
			}
		}
		return nil
	})
}

// CIDRsContain reports whether ip falls inside any of the given CIDR rules. Used by the
// allowlist-PUT lockout guard before the new set is persisted (doc 05 §6).
func CIDRsContain(rules []IPRule, ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, r := range rules {
		if _, network, err := net.ParseCIDR(r.CIDR); err == nil && network.Contains(parsed) {
			return true
		}
	}
	return false
}

// Allowed reports whether ip is permitted for the caller's tenant: no rows means no restriction;
// otherwise the address must fall inside at least one CIDR (doc 05 §6). A malformed stored CIDR is
// skipped rather than allowed to lock the tenant out.
func (a *IPAllow) Allowed(ctx context.Context, ip string) (bool, error) {
	parsed := net.ParseIP(ip)
	rules, err := a.List(ctx)
	if err != nil {
		return false, err
	}
	if len(rules) == 0 {
		return true, nil // no allowlist configured => unrestricted
	}
	if parsed == nil {
		return false, nil
	}
	for _, r := range rules {
		_, network, e := net.ParseCIDR(r.CIDR)
		if e != nil {
			continue
		}
		if network.Contains(parsed) {
			return true, nil
		}
	}
	return false, nil
}
