// Package pgstore is the PostgreSQL-backed Store (all three ledgers), with tenant isolation
// enforced at the DATABASE via row-level security (gate G1, docs/06, docs/18 §1):
//
//	FieldVersions      -> G5 (append-only provenance)
//	IdempotencyLedger  -> G2 (exactly-once-effective provider calls; INSERT ON CONFLICT)
//	CostLedger         -> G4 (cost ceiling; a single guarded UPDATE is the atomic reservation)
//
// The store never accepts a tenant id as an argument: it reads the tenant from the request
// principal (internal/tenant) and, inside each transaction, binds it as the GUC
// `app.current_tenant`. Every RLS policy in migration 0001 scopes rows to that GUC, so even a
// bug cannot read or write another tenant's rows — the database returns zero rows / rejects
// the write. Connections come from a bounded pool (one tenant-GUC set per transaction).
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

// ErrInvalidFieldValue backs the G5 rejection of a bare, provenance-less value (mirrors the
// in-memory store and the schema CHECK constraint).
var ErrInvalidFieldValue = errors.New("pgstore: field value fails G5 validity (missing value or provenance)")

// Store is a Postgres-backed store.Store.
type Store struct {
	pool *pg.Pool
}

// New wraps a connection pool.
func New(pool *pg.Pool) *Store { return &Store{pool: pool} }

// Ping checks that a pooled connection can round-trip a trivial query — used by the /readyz
// readiness probe so the service reports "ready" only when its datastore is actually reachable.
func (s *Store) Ping(ctx context.Context) error {
	c, err := s.pool.Get(ctx)
	if err != nil {
		return err
	}
	broken := false
	defer func() { s.pool.Put(c, broken) }()
	if err := c.Exec("select 1"); err != nil {
		broken = true
		return err
	}
	return nil
}

// Open builds a pool of up to maxConns connections and returns a Store.
func Open(cfg pg.Config, maxConns int) (*Store, error) {
	// Validate connectivity eagerly so misconfiguration fails fast.
	c, err := pg.Connect(cfg)
	if err != nil {
		return nil, err
	}
	c.Close()
	return New(pg.NewPool(cfg, maxConns)), nil
}

// Close closes the pool.
func (s *Store) Close() error { s.pool.Close(); return nil }

// tx runs fn inside a transaction with app.current_tenant bound to the principal's tenant
// (G1). The GUC is set LOCAL, so it is scoped to this transaction only. The connection is
// returned to the pool afterward (or discarded if the transaction left it in a bad state).
func (s *Store) tx(ctx context.Context, fn func(*pg.Conn) error) error {
	ten, err := tenant.TenantID(ctx)
	if err != nil {
		return err // fail-closed: no principal => no access
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
		_ = c.Exec("rollback") // op failed; roll back but the connection is still usable
		return ferr
	}
	if err := c.Exec("commit"); err != nil {
		broken = true
		return err
	}
	return nil
}

// --- FieldVersions (G5) ---

// Append persists a resolved FieldValue. tenant_id is taken from the GUC, never a caller
// argument, so RLS WITH CHECK confines the row to the caller's own tenant partition.
func (s *Store) Append(ctx context.Context, subjectID string, v domain.FieldValue) error {
	if !v.Valid() {
		return ErrInvalidFieldValue
	}
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into field_versions
			(tenant_id, subject_id, field, value, confidence, provider, cost_credits, obs_confidence, idempotency_key, observed_at)
			values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			subjectID, string(v.Field), v.Value, float64(v.Confidence), v.Prov.Provider,
			int64(v.Prov.CostCredits), float64(v.Prov.Confidence), v.Prov.IdempotencyKey, v.Prov.ObservedAt)
	})
}

// Current returns the highest-confidence value per field for a subject, within the tenant.
func (s *Store) Current(ctx context.Context, subjectID string) (map[domain.Field]domain.FieldValue, error) {
	out := map[domain.Field]domain.FieldValue{}
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select distinct on (field)
				field, value, confidence, provider, cost_credits, obs_confidence, idempotency_key, observed_at
			from field_versions where subject_id = $1
			order by field, confidence desc`, subjectID)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			fv := scanFieldValue(row)
			out[fv.Field] = fv
		}
		return nil
	})
	return out, err
}

// History returns every retained observation for a subject+field (winners and losers).
func (s *Store) History(ctx context.Context, subjectID string, f domain.Field) ([]domain.FieldValue, error) {
	var out []domain.FieldValue
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select
				field, value, confidence, provider, cost_credits, obs_confidence, idempotency_key, observed_at
			from field_versions where subject_id = $1 and field = $2
			order by confidence desc`, subjectID, string(f))
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, scanFieldValue(row))
		}
		return nil
	})
	return out, err
}

// --- IdempotencyLedger (G2) ---

// Lookup returns a prior terminal result for key within the caller's tenant, if any.
func (s *Store) Lookup(ctx context.Context, key string) (provider.Result, bool, error) {
	var out provider.Result
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams("select result from idempotency_ledger where idempotency_key = $1", key)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return nil
		}
		if err := json.Unmarshal([]byte(*res.Rows[0][0]), &out); err != nil {
			return err
		}
		found = true
		return nil
	})
	return out, found, err
}

// Record stores the terminal result for key. A repeat is a no-op (first writer wins), so
// concurrent retries converge on one paid result (G2).
func (s *Store) Record(ctx context.Context, key string, res provider.Result) error {
	payload, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into idempotency_ledger (tenant_id, idempotency_key, result)
			values (current_setting('app.current_tenant'), $1, $2::jsonb)
			on conflict (tenant_id, idempotency_key) do nothing`, key, string(payload))
	})
}

// --- CostLedger (G4) ---

// Reserve atomically adds amount to jobID's committed spend iff the new total does not exceed
// ceiling, returning the new total; otherwise it makes no change and returns
// store.ErrCeilingExceeded. The guarded UPDATE ... WHERE is the atomic reservation: a row
// lock serializes concurrent reservations, so the ceiling is never breached even transiently.
func (s *Store) Reserve(ctx context.Context, jobID string, amount, ceiling domain.Credits) (domain.Credits, error) {
	var committed domain.Credits
	err := s.tx(ctx, func(c *pg.Conn) error {
		// Ensure the row exists at 0 (no ceiling effect), then take the guarded add path.
		if err := c.ExecParams(`insert into cost_ledger (tenant_id, job_id, committed)
			values (current_setting('app.current_tenant'), $1, 0)
			on conflict (tenant_id, job_id) do nothing`, jobID); err != nil {
			return err
		}
		res, err := c.QueryParams(`update cost_ledger set committed = committed + $2
			where tenant_id = current_setting('app.current_tenant') and job_id = $1
			  and committed + $2 <= $3
			returning committed`, jobID, int64(amount), int64(ceiling))
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || res.Rows[0][0] == nil {
			return store.ErrCeilingExceeded // no row updated => would exceed
		}
		n, _ := strconv.ParseInt(*res.Rows[0][0], 10, 64)
		committed = domain.Credits(n)
		return nil
	})
	return committed, err
}

// Release refunds a prior reservation for jobID (never below zero).
func (s *Store) Release(ctx context.Context, jobID string, amount domain.Credits) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update cost_ledger set committed = greatest(0, committed - $2)
			where tenant_id = current_setting('app.current_tenant') and job_id = $1`,
			jobID, int64(amount))
	})
}

// Committed returns the amount charged so far for jobID within the tenant.
func (s *Store) Committed(ctx context.Context, jobID string) (domain.Credits, error) {
	var n int64
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams("select committed from cost_ledger where job_id = $1", jobID)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			n, _ = strconv.ParseInt(*res.Rows[0][0], 10, 64)
		}
		return nil
	})
	return domain.Credits(n), err
}

// --- row scanning ---

func scanFieldValue(row []*string) domain.FieldValue {
	get := func(i int) string {
		if i < len(row) && row[i] != nil {
			return *row[i]
		}
		return ""
	}
	conf, _ := strconv.ParseFloat(get(2), 64)
	cost, _ := strconv.ParseInt(get(4), 10, 64)
	obs, _ := strconv.ParseFloat(get(5), 64)
	return domain.FieldValue{
		Field:      domain.Field(get(0)),
		Value:      get(1),
		Confidence: domain.Confidence(conf),
		Prov: domain.Provenance{
			Provider:       get(3),
			CostCredits:    domain.Credits(cost),
			Confidence:     domain.Confidence(obs),
			IdempotencyKey: get(6),
			ObservedAt:     parseTS(get(7)),
		},
	}
}

func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// compile-time assertion that Store is a full store.Store.
var _ store.Store = (*Store)(nil)
