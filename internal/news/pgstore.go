package news

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Store is a Postgres-backed store for news items and market signals (migration 0018). Tenant isolation
// (G1) is enforced at the DATABASE: the store NEVER takes a tenant id — it reads the principal from ctx
// and binds app.current_tenant per transaction, so RLS confines every read/write to the caller's tenant
// (same mechanism as internal/intent / internal/research). The app role has no BYPASSRLS.
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

// SaveItems inserts news items, idempotent per (tenant, account, url) — a redelivered item is a no-op.
// tenant_id comes from the GUC, so RLS WITH CHECK confines rows to the caller's tenant.
func (s *Store) SaveItems(ctx context.Context, items []NewsItem) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		for _, it := range items {
			var pub any // nil => NULL published_at (unknown)
			if !it.PublishedAt.IsZero() {
				pub = it.PublishedAt
			}
			if err := c.ExecParams(`insert into news_items
				(tenant_id, account, source, title, url, topic, published_at)
				values (current_setting('app.current_tenant'), $1, $2, $3, $4, $5, $6)
				on conflict (tenant_id, account, url) do nothing`,
				it.Account, it.Source, it.Title, it.URL, it.Topic, pub); err != nil {
				return err
			}
		}
		return nil
	})
}

// ItemsByAccount returns stored news items for an account (most recent first), within the tenant.
func (s *Store) ItemsByAccount(ctx context.Context, account string) ([]NewsItem, error) {
	var out []NewsItem
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select account, source, title, url, topic, published_at
			from news_items where account = $1 order by published_at desc nulls last, id desc`, account)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, NewsItem{
				Account:     strval(row, 0),
				Source:      strval(row, 1),
				Title:       strval(row, 2),
				URL:         strval(row, 3),
				Topic:       strval(row, 4),
				PublishedAt: parseTime(strval(row, 5)),
			})
		}
		return nil
	})
	return out, err
}

// SaveSignals inserts market signals (append-only). Detail defaults to {}; observed_at defaults to now().
func (s *Store) SaveSignals(ctx context.Context, signals []MarketSignal) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		for _, sig := range signals {
			detail := sig.Detail
			if len(detail) == 0 {
				detail = json.RawMessage("{}")
			}
			var obs any // nil => COALESCE picks now()
			if !sig.ObservedAt.IsZero() {
				obs = sig.ObservedAt
			}
			if err := c.ExecParams(`insert into market_signals
				(tenant_id, account, signal_type, magnitude, detail, observed_at)
				values (current_setting('app.current_tenant'), $1, $2, $3, $4::jsonb, coalesce($5::timestamptz, now()))`,
				sig.Account, sig.SignalType, sig.Magnitude, string(detail), obs); err != nil {
				return err
			}
		}
		return nil
	})
}

// SignalsByAccount returns stored market signals for an account (most recent first), within the tenant.
func (s *Store) SignalsByAccount(ctx context.Context, account string) ([]MarketSignal, error) {
	var out []MarketSignal
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select account, signal_type, magnitude, detail, observed_at
			from market_signals where account = $1 order by observed_at desc, id desc`, account)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, MarketSignal{
				Account:    strval(row, 0),
				SignalType: strval(row, 1),
				Magnitude:  floatval(row, 2),
				Detail:     json.RawMessage(strval(row, 3)),
				ObservedAt: parseTime(strval(row, 4)),
			})
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

// parseTime parses a Postgres ISO timestamptz text value (or an RFC3339 value); zero Time on failure.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05-07:00",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
