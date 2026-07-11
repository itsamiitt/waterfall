// Package intent is the Intent dashboard's read-model service (Slice 26): a thin, tenant-scoped read
// surface over intent_scores (migration 0016, ADR-0027) for the admin UI. It rides the dashboard's
// dual-GUC RLS seam (db.Store.Tx binds app.current_tenant + app.current_role from the verified
// Principal, ADR-0020), so a tenant_admin / tenant_user sees only their own Tenant's computed intent.
//
// Operator cross-Tenant visibility rides the enumerated operator SELECT policy on intent_scores
// (migration 0017, mirroring 0009's tenant_usage_* operator-read): rbac grants the operator
// DecisionAllow for intent.read, and the additive policy lets an operator's dual-GUC transaction read
// across Tenants. For an operator the List roll-up (group by account) is therefore platform-wide —
// same-named accounts in different Tenants collapse to one summary row; that coarse overview is
// intended. A tenant_admin / tenant_user is still confined to their own Tenant by *_tenant_isolation.
package intent

import (
	"context"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// AccountScore is a dashboard read model of one computed intent class score.
type AccountScore struct {
	Class       string  `json:"class"`
	Score       float64 `json:"score"`
	Confidence  float64 `json:"confidence"`
	SignalCount int     `json:"signal_count"`
	ConfigVer   string  `json:"config_version"`
	ComputedAt  string  `json:"computed_at"`
}

// AccountSummary is a dashboard list entry: an account with its strongest intent class.
type AccountSummary struct {
	Account  string  `json:"account"`
	TopClass string  `json:"top_class"`
	TopScore float64 `json:"top_score"`
	Classes  int     `json:"classes"`
}

// Service is the intent-dashboard read model over intent_scores, tenant-scoped via the dual-GUC RLS
// (db.Store.Tx). It never takes a tenant id — the tenant flows from the ctx Principal.
type Service struct {
	store *db.Store
}

// NewService wraps a dashboard db.Store.
func NewService(store *db.Store) *Service { return &Service{store: store} }

// Account returns the per-class scores for an account within the caller's Tenant (score desc).
func (s *Service) Account(ctx context.Context, account string) ([]AccountScore, error) {
	out := []AccountScore{}
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select signal_class, score, confidence, signal_count, config_version, computed_at
			from intent_scores where account = $1 order by score desc, signal_class asc`, account)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, AccountScore{
				Class:       cell(row, 0),
				Score:       fcell(row, 1),
				Confidence:  fcell(row, 2),
				SignalCount: icell(row, 3),
				ConfigVer:   cell(row, 4),
				ComputedAt:  cell(row, 5),
			})
		}
		return nil
	})
	return out, err
}

// List returns accounts with computed intent within the caller's Tenant, strongest first (capped).
func (s *Service) List(ctx context.Context, limit int) ([]AccountSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := []AccountSummary{}
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select account,
				(array_agg(signal_class order by score desc))[1] as top_class,
				max(score) as top_score,
				count(*) as classes
			from intent_scores
			group by account
			order by max(score) desc, account asc
			limit $1`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, AccountSummary{
				Account:  cell(row, 0),
				TopClass: cell(row, 1),
				TopScore: fcell(row, 2),
				Classes:  icell(row, 3),
			})
		}
		return nil
	})
	return out, err
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

func icell(row []*string, i int) int {
	n, _ := strconv.Atoi(cell(row, i))
	return n
}
