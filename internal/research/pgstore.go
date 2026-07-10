package research

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Store is a Postgres-backed store for research Dossiers and their queryable provenance (migration
// 0015, ADR-0028). Tenant isolation (G1) is enforced at the DATABASE: the store NEVER takes a tenant
// id as an argument — it reads the principal from ctx and binds app.current_tenant per transaction,
// so RLS confines every read/write to the caller's tenant (same mechanism as internal/pgstore). The
// app role has no BYPASSRLS, so a bug cannot cross tenants.
type Store struct {
	pool *pg.Pool
}

// NewStore wraps a connection pool.
func NewStore(pool *pg.Pool) *Store { return &Store{pool: pool} }

// OpenStore validates connectivity and returns a pooled Store.
func OpenStore(cfg pg.Config, maxConns int) (*Store, error) {
	c, err := pg.Connect(cfg)
	if err != nil {
		return nil, err
	}
	c.Close()
	return NewStore(pg.NewPool(cfg, maxConns)), nil
}

// Close closes the pool.
func (s *Store) Close() error { s.pool.Close(); return nil }

// tx runs fn inside a transaction with app.current_tenant bound (LOCAL) to the principal's tenant
// (G1). Fail-closed: no principal ⇒ no access.
func (s *Store) tx(ctx context.Context, fn func(*pg.Conn) error) error {
	ten, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	c, err := s.pool.Get(ctx)
	if err != nil {
		return err
	}
	broken := false
	defer func() { s.pool.Put(c, broken) }()
	if err := c.Exec("begin"); err != nil {
		broken = true
		return err
	}
	if err := c.ExecParams("select set_config('app.current_tenant', $1, true)", ten); err != nil {
		_ = c.Exec("rollback")
		return err
	}
	if ferr := fn(c); ferr != nil {
		_ = c.Exec("rollback")
		return ferr
	}
	if err := c.Exec("commit"); err != nil {
		broken = true
		return err
	}
	return nil
}

// SaveDossier upserts the Dossier (latest per (tenant, dossierID)) and replaces its provenance rows
// (research_sources, G5). tenant_id comes from the GUC, so RLS WITH CHECK confines rows to the
// caller's tenant. Provenance rows with an invalid source_type or empty field/provider are skipped
// (defensive against the schema CHECK / NOT NULL).
func (s *Store) SaveDossier(ctx context.Context, dossierID, subjectKey string, d Dossier) error {
	blob, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(`insert into research_dossiers
			(tenant_id, dossier_id, subject_key, dossier, overall_confidence, config_version, freshness_at)
			values (current_setting('app.current_tenant'), $1, $2, $3::jsonb, $4, $5, now())
			on conflict (tenant_id, dossier_id) do update set
				dossier = excluded.dossier, subject_key = excluded.subject_key,
				overall_confidence = excluded.overall_confidence, config_version = excluded.config_version,
				freshness_at = now()`,
			dossierID, subjectKey, string(blob), d.Confidence.Overall, strconv.Itoa(d.Metadata.ConfigVersion)); err != nil {
			return err
		}
		if err := c.ExecParams("delete from research_sources where dossier_id = $1", dossierID); err != nil {
			return err
		}
		for _, src := range d.Provenance {
			if !validSourceType(src.SourceType) || src.Field == "" || src.Provider == "" {
				continue
			}
			if err := c.ExecParams(`insert into research_sources
				(tenant_id, dossier_id, field, provider, source_type, cost_credits, idem_key, confidence)
				values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5, $6, $7)`,
				dossierID, src.Field, src.Provider, src.SourceType, int64(src.Cost), src.IdemKey, src.Confidence); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetDossier returns the stored Dossier for dossierID within the caller's tenant.
func (s *Store) GetDossier(ctx context.Context, dossierID string) (Dossier, bool, error) {
	return s.queryDossier(ctx, "select dossier from research_dossiers where dossier_id = $1", dossierID)
}

// LatestBySubject returns the freshest stored Dossier for a subject (GET /v1/dossiers/{domain}).
func (s *Store) LatestBySubject(ctx context.Context, subjectKey string) (Dossier, bool, error) {
	return s.queryDossier(ctx,
		"select dossier from research_dossiers where subject_key = $1 order by freshness_at desc limit 1", subjectKey)
}

func (s *Store) queryDossier(ctx context.Context, sql string, arg string) (Dossier, bool, error) {
	var d Dossier
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, arg)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		if err := json.Unmarshal([]byte(*res.Rows[0][0]), &d); err != nil {
			return err
		}
		found = true
		return nil
	})
	return d, found, err
}

func validSourceType(t string) bool {
	return t == SourceAPI || t == SourceDataset || t == SourceAI
}
