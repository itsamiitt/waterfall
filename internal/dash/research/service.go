// Package research is the Research dashboard's read-model service (Slice 26): a tenant-scoped read
// surface over research_dossiers (migration 0015, ADR-0028) for the admin UI. It rides the dashboard
// dual-GUC RLS seam (db.Store.Tx binds app.current_tenant + app.current_role from the verified
// Principal, ADR-0020), so a tenant_admin / tenant_user sees only their own Tenant's dossiers.
//
// Operator cross-Tenant visibility rides the enumerated operator SELECT policy on research_dossiers
// (migration 0017, mirroring 0009's tenant_usage_* operator-read): rbac grants the operator
// DecisionAllow for research.read, and the additive policy lets an operator's dual-GUC transaction
// list/read dossiers across Tenants, while a tenant_admin / tenant_user stays confined to their own
// Tenant by *_tenant_isolation. The web feature is a follow-on increment.
package research

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// DossierSummary is a lightweight dashboard list entry (no full Dossier blob).
type DossierSummary struct {
	DossierID   string  `json:"dossier_id"`
	SubjectKey  string  `json:"subject_key"`
	Confidence  float64 `json:"overall_confidence"`
	ConfigVer   string  `json:"config_version"`
	FreshnessAt string  `json:"freshness_at"`
}

// Service is the research-dashboard read model over research_dossiers, tenant-scoped via the
// dual-GUC RLS (db.Store.Tx). It never takes a tenant id — the tenant flows from the ctx Principal.
type Service struct {
	store *db.Store
}

// NewService wraps a dashboard db.Store.
func NewService(store *db.Store) *Service { return &Service{store: store} }

// List returns dossier summaries for the caller's Tenant, freshest first (capped).
func (s *Service) List(ctx context.Context, limit int) ([]DossierSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []DossierSummary{}
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select dossier_id, subject_key, overall_confidence, config_version, freshness_at
			from research_dossiers order by freshness_at desc, dossier_id asc limit $1`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, DossierSummary{
				DossierID:   cell(row, 0),
				SubjectKey:  cell(row, 1),
				Confidence:  fcell(row, 2),
				ConfigVer:   cell(row, 3),
				FreshnessAt: cell(row, 4),
			})
		}
		return nil
	})
	return out, err
}

// RunSummary is a dashboard read model of one research run's lifecycle (research_runs, migration 0015).
type RunSummary struct {
	RunID      string `json:"run_id"`
	SubjectKey string `json:"subject_key"`
	Status     string `json:"status"`
	ConfigVer  string `json:"config_version"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Runs returns research run lifecycle rows for the caller's Tenant, newest first (capped). A tenant_admin
// sees its own runs (Class-T *_tenant_isolation); an operator reads runs across Tenants via the additive
// operator-read policy (migration 0020, mirroring 0017 for dossiers/scores).
func (s *Service) Runs(ctx context.Context, limit int) ([]RunSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []RunSummary{}
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select run_id, subject_key, status, config_version, created_at, updated_at
			from research_runs order by created_at desc, run_id asc limit $1`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, RunSummary{
				RunID:      cell(row, 0),
				SubjectKey: cell(row, 1),
				Status:     cell(row, 2),
				ConfigVer:  cell(row, 3),
				CreatedAt:  cell(row, 4),
				UpdatedAt:  cell(row, 5),
			})
		}
		return nil
	})
	return out, err
}

// Dossier returns the full stored Dossier JSON for the caller's Tenant, or ok=false if absent.
func (s *Service) Dossier(ctx context.Context, dossierID string) (json.RawMessage, bool, error) {
	var out json.RawMessage
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select dossier from research_dossiers where dossier_id = $1`, dossierID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		out = json.RawMessage(*res.Rows[0][0])
		found = true
		return nil
	})
	return out, found, err
}

func cell(row []*string, i int) string {
	if i < len(row) && row[i] != nil {
		return *row[i]
	}
	return ""
}

func fcell(row []*string, i int) float64 {
	f, _ := strconv.ParseFloat(cell(row, i), 64)
	return f
}
