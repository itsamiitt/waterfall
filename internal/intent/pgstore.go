package intent

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Store is a Postgres-backed store for computed intent class scores (migration 0016). Tenant
// isolation (G1) is enforced at the DATABASE: it never takes a tenant id — it binds
// app.current_tenant per transaction from the ctx Principal, so RLS confines every read/write to the
// caller's tenant (same mechanism as internal/pgstore / internal/research). The app role has no
// BYPASSRLS.
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

// tx runs fn with app.current_tenant bound (LOCAL) to the principal's tenant (G1). Fail-closed.
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

// SaveScores upserts the computed class scores for an account (latest per (account, class)),
// pinning the config version used to compute them (ADR-0027 reproducibility). tenant_id comes from
// the GUC, so RLS WITH CHECK confines the rows to the caller's tenant.
func (s *Store) SaveScores(ctx context.Context, account, configVersion string, scores []ClassScore) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		for _, cs := range scores {
			reasoning, err := json.Marshal(cs.Reasoning)
			if err != nil {
				return err
			}
			if err := c.ExecParams(`insert into intent_scores
				(tenant_id, account, signal_class, score, confidence, signal_count, reasoning, config_version, computed_at)
				values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5, $6::jsonb, $7, now())
				on conflict (tenant_id, account, signal_class) do update set
					score = excluded.score, confidence = excluded.confidence, signal_count = excluded.signal_count,
					reasoning = excluded.reasoning, config_version = excluded.config_version, computed_at = now()`,
				account, string(cs.Class), cs.Score, cs.Confidence, cs.SignalCount, string(reasoning), configVersion); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetByAccount returns the stored class scores for an account (score desc), within the tenant.
func (s *Store) GetByAccount(ctx context.Context, account string) ([]ClassScore, error) {
	var out []ClassScore
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select signal_class, score, confidence, signal_count, reasoning
			from intent_scores where account = $1 order by score desc, signal_class asc`, account)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			cs := ClassScore{
				Class:       Class(strval(row, 0)),
				Score:       floatval(row, 1),
				Confidence:  floatval(row, 2),
				SignalCount: intval(row, 3),
				Reasoning:   []Contribution{},
			}
			if r := strval(row, 4); r != "" {
				_ = json.Unmarshal([]byte(r), &cs.Reasoning)
			}
			out = append(out, cs)
		}
		return nil
	})
	return out, err
}

func strval(row []*string, i int) string {
	if i < len(row) && row[i] != nil {
		return *row[i]
	}
	return ""
}

func floatval(row []*string, i int) float64 {
	f, _ := strconv.ParseFloat(strval(row, i), 64)
	return f
}

func intval(row []*string, i int) int {
	n, _ := strconv.Atoi(strval(row, i))
	return n
}
