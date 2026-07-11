package crm

import (
	"context"
	"encoding/json"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Store is a Postgres-backed store for CRM connections, field maps, and the idempotent push ledger
// (migration 0019). Tenant isolation (G1) is enforced at the DATABASE: the store NEVER takes a tenant id
// — it reads the principal from ctx and binds app.current_tenant per transaction, so RLS confines every
// read/write to the caller's tenant (same mechanism as internal/news / internal/intent). The app role has
// no BYPASSRLS, so Tenant A can never push into Tenant B's connection.
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

// tx runs fn inside a transaction with app.current_tenant bound (LOCAL) to the principal's tenant (G1).
// Fail-closed: no principal ⇒ no access.
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

// SaveConnection upserts a CRM connection (latest per (tenant, connection_id)). SecretRef is stored as an
// envelope reference only; no plaintext token ever reaches this table.
func (s *Store) SaveConnection(ctx context.Context, c Connection) error {
	return s.tx(ctx, func(conn *pg.Conn) error {
		return conn.ExecParams(`insert into crm_connections
			(tenant_id, connection_id, provider, status, secret_ref, config, updated_at)
			values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5::jsonb, now())
			on conflict (tenant_id, connection_id) do update set
				provider = excluded.provider, status = excluded.status, secret_ref = excluded.secret_ref,
				config = excluded.config, updated_at = now()`,
			c.ConnectionID, c.Provider, statusOr(c.Status, "active"), c.SecretRef, jsonOr(c.Config))
	})
}

// GetConnection returns a connection by id within the caller's tenant.
func (s *Store) GetConnection(ctx context.Context, connectionID string) (Connection, bool, error) {
	var out Connection
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select connection_id, provider, status, secret_ref, config
			from crm_connections where connection_id = $1`, connectionID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = scanConnection(res.Rows[0])
		found = true
		return nil
	})
	return out, found, err
}

// ListConnections returns all connections within the caller's tenant (newest first).
func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	var out []Connection
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select connection_id, provider, status, secret_ref, config
			from crm_connections order by created_at desc, connection_id asc`)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, scanConnection(row))
		}
		return nil
	})
	return out, err
}

// SaveFieldMap upserts a versioned field map for a connection.
func (s *Store) SaveFieldMap(ctx context.Context, m FieldMap) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into crm_field_maps
			(tenant_id, connection_id, version, mapping)
			values (current_setting('app.current_tenant'), $1, $2, $3::jsonb)
			on conflict (tenant_id, connection_id, version) do update set mapping = excluded.mapping`,
			m.ConnectionID, m.Version, jsonOr(m.Mapping))
	})
}

// LatestFieldMap returns the highest-version field map for a connection within the caller's tenant.
func (s *Store) LatestFieldMap(ctx context.Context, connectionID string) (FieldMap, bool, error) {
	var out FieldMap
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select connection_id, version, mapping
			from crm_field_maps where connection_id = $1 order by version desc limit 1`, connectionID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = FieldMap{
			ConnectionID: strval(res.Rows[0], 0),
			Version:      intval(res.Rows[0], 1),
			Mapping:      json.RawMessage(strval(res.Rows[0], 2)),
		}
		found = true
		return nil
	})
	return out, found, err
}

// RecordPush records a push in the idempotent ledger (G2, ADR-0030). It returns true when the push was
// newly recorded and false when a row with the same (tenant, idem_key) already existed — i.e. a redelivery
// that must NOT re-write the CRM. The caller performs the actual egress push only when this returns true.
func (s *Store) RecordPush(ctx context.Context, p PushRecord) (bool, error) {
	inserted := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`insert into crm_push_ledger
			(tenant_id, connection_id, idem_key, record, field_map_version, dossier_version, status, pushed_at)
			values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5, $6, now())
			on conflict (tenant_id, idem_key) do nothing
			returning id`,
			p.ConnectionID, p.IdemKey, p.Record, p.FieldMapVersion, p.DossierVersion, statusOr(p.Status, "pushed"))
		if err != nil {
			return err
		}
		inserted = len(res.Rows) > 0
		return nil
	})
	return inserted, err
}

// DeletePush releases a push-ledger claim by idem_key (used on push failure so the push is retryable).
// RLS scopes the delete to the caller's tenant.
func (s *Store) DeletePush(ctx context.Context, idemKey string) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams("delete from crm_push_ledger where idem_key = $1", idemKey)
	})
}

// MarkErasurePending flags every not-yet-erased ledger row for a record as erasure-obligated (DSAR
// cascade, ADR-0030 / 09 §5): the downstream CRM erasure obligation is recorded so a DSAR delete
// propagates to what was pushed. Returns the number of rows newly marked.
func (s *Store) MarkErasurePending(ctx context.Context, record string) (int, error) {
	n := 0
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`update crm_push_ledger set status = 'erasure_pending'
			where record = $1 and status not in ('erasure_pending', 'erased') returning id`, record)
		if err != nil {
			return err
		}
		n = len(res.Rows)
		return nil
	})
	return n, err
}

func scanConnection(row []*string) Connection {
	return Connection{
		ConnectionID: strval(row, 0),
		Provider:     strval(row, 1),
		Status:       strval(row, 2),
		SecretRef:    strval(row, 3),
		Config:       json.RawMessage(strval(row, 4)),
	}
}

func statusOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func jsonOr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func strval(row []*string, i int) string {
	if i < len(row) && row[i] != nil {
		return *row[i]
	}
	return ""
}

func intval(row []*string, i int) int {
	var n int
	for _, ch := range strval(row, i) {
		if ch < '0' || ch > '9' {
			return n
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
