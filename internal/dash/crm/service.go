// Package crm is the CRM outbound dashboard's read-model service (Slice 27, ADR-0030): a tenant-scoped read
// surface over crm_connections (migration 0019) for the admin UI. It rides the dashboard dual-GUC RLS seam
// (db.Store.Tx binds app.current_tenant + app.current_role from the verified Principal), so a tenant_admin
// sees only their own Tenant's CRM connections. The projection intentionally OMITS secret_ref and config —
// the dashboard shows WHICH CRMs a Tenant pushes to and their status, never a credential reference. The
// configure/trigger write path (with envelope-sealed secrets) and the DSAR erasure cascade are follow-ons.
package crm

import (
	"context"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// ConnectionSummary is a dashboard read model of one configured CRM connection (no credential material).
type ConnectionSummary struct {
	ConnectionID string `json:"connection_id"`
	Provider     string `json:"provider"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// Service is the CRM-dashboard read model over crm_connections, tenant-scoped via the dual-GUC RLS
// (db.Store.Tx). It never takes a tenant id — the tenant flows from the ctx Principal.
type Service struct {
	store *db.Store
}

// NewService wraps a dashboard db.Store.
func NewService(store *db.Store) *Service { return &Service{store: store} }

// List returns the caller's Tenant's CRM connections (newest first, capped).
func (s *Service) List(ctx context.Context, limit int) ([]ConnectionSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []ConnectionSummary{}
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select connection_id, provider, status, created_at, updated_at
			from crm_connections order by created_at desc, connection_id asc limit $1`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, scan(row))
		}
		return nil
	})
	return out, err
}

// Get returns one CRM connection by id within the caller's Tenant.
func (s *Service) Get(ctx context.Context, connectionID string) (ConnectionSummary, bool, error) {
	var out ConnectionSummary
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select connection_id, provider, status, created_at, updated_at
			from crm_connections where connection_id = $1`, connectionID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = scan(res.Rows[0])
		found = true
		return nil
	})
	return out, found, err
}

func scan(row []*string) ConnectionSummary {
	return ConnectionSummary{
		ConnectionID: cell(row, 0),
		Provider:     cell(row, 1),
		Status:       cell(row, 2),
		CreatedAt:    cell(row, 3),
		UpdatedAt:    cell(row, 4),
	}
}

func cell(row []*string, i int) string {
	if i < len(row) && row[i] != nil {
		return *row[i]
	}
	return ""
}
