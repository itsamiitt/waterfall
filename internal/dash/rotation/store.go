package rotation

import (
	"context"
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// poolKeyRow is the runtime image of one member Provider Key, read from provider_keys joined on
// key_pool_members. It carries exactly the columns selection + leasing need (never plaintext).
type poolKeyRow struct {
	ID               string
	EnvelopeID       string
	Weight           int
	Priority         *int64
	Region           string
	Status           string
	LatencyEWMA      *float64
	SuccessEWMA      *float64
	CreditsRemaining *int64
	DailyLimit       *int64
}

// PoolData is a pool's identity + strategy + member rows, the input to buildPoolState.
type PoolData struct {
	Selector string
	PoolID   string
	Strategy string
	Params   string
	Status   string
	Rows     []poolKeyRow
}

// TriggerRow is a rotation_triggers row (GET/PUT /v1/admin/rotation/triggers, doc 04 §2.5).
type TriggerRow struct {
	Trigger    string `json:"trigger"`
	Thresholds string `json:"thresholds,omitempty"` // jsonb text ("" = NULL)
	CooldownS  *int64 `json:"cooldown_s,omitempty"`
	Enabled    bool   `json:"enabled"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// Store is the persistence seam the Engine consumes (consumer-side interface, satisfied by
// *pgStore). Every method runs through db.Store.PlatformTx — provider_keys / key_pools /
// key_budgets / rotation_triggers are Class P platform tables (doc 05 §3.1).
type Store interface {
	LoadPoolBySelector(ctx context.Context, selector string) (PoolData, bool, error)
	LoadPoolByID(ctx context.Context, poolID string) (PoolData, bool, error)
	StatusStore
	leaser
	reconcileStore
	ListTriggers(ctx context.Context) ([]TriggerRow, error)
	GetTrigger(ctx context.Context, name string) (TriggerRow, bool, error)
	UpsertTrigger(ctx context.Context, tr TriggerRow, actor string) error
}

// pgStore is the Class-P persistence layer for module 4.
type pgStore struct {
	db *db.Store
}

func newPGStore(store *db.Store) *pgStore { return &pgStore{db: store} }

// NewStore builds the Class-P rotation Store over the shared db.Store.
func NewStore(store *db.Store) Store { return newPGStore(store) }

var _ Store = (*pgStore)(nil)

const poolKeyColumns = `pk.id, pk.secret_envelope_id, pk.weight, pk.priority, pk.region, pk.status,
	pk.latency_ewma_ms, pk.success_ewma, pk.credits_remaining, pk.daily_limit`

func scanPoolKey(r []*string) poolKeyRow {
	return poolKeyRow{
		ID:               s(r[0]),
		EnvelopeID:       s(r[1]),
		Weight:           int(i64(r[2])),
		Priority:         pint(r[3]),
		Region:           s(r[4]),
		Status:           s(r[5]),
		LatencyEWMA:      pfloat(r[6]),
		SuccessEWMA:      pfloat(r[7]),
		CreditsRemaining: pint(r[8]),
		DailyLimit:       pint(r[9]),
	}
}

// LoadPoolBySelector reads the pool identified by provider_id:name and its member rows.
func (st *pgStore) LoadPoolBySelector(ctx context.Context, selector string) (PoolData, bool, error) {
	providerID, name, ok := splitSelector(selector)
	if !ok {
		return PoolData{}, false, nil
	}
	var out PoolData
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id, strategy, coalesce(strategy_params::text,''), status
			   from key_pools where provider_id = $1 and name = $2`, providerID, name)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		found = true
		out = PoolData{
			Selector: selector,
			PoolID:   s(res.Rows[0][0]),
			Strategy: s(res.Rows[0][1]),
			Params:   s(res.Rows[0][2]),
			Status:   s(res.Rows[0][3]),
		}
		rows, err := loadMembers(c, out.PoolID)
		if err != nil {
			return err
		}
		out.Rows = rows
		return nil
	})
	return out, found, err
}

// LoadPoolByID reads a pool by its uuid (for the pool-scoped debug endpoints).
func (st *pgStore) LoadPoolByID(ctx context.Context, poolID string) (PoolData, bool, error) {
	var out PoolData
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select provider_id, name, strategy, coalesce(strategy_params::text,''), status
			   from key_pools where id = $1`, poolID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		found = true
		out = PoolData{
			Selector: s(res.Rows[0][0]) + ":" + s(res.Rows[0][1]),
			PoolID:   poolID,
			Strategy: s(res.Rows[0][2]),
			Params:   s(res.Rows[0][3]),
			Status:   s(res.Rows[0][4]),
		}
		rows, err := loadMembers(c, poolID)
		if err != nil {
			return err
		}
		out.Rows = rows
		return nil
	})
	return out, found, err
}

// loadMembers reads a pool's member Provider Key rows, ordered so ordered-walk strategies are
// deterministic (priority ASC NULLS LAST, then id).
func loadMembers(c *pg.Conn, poolID string) ([]poolKeyRow, error) {
	res, err := c.QueryParams(
		`select `+poolKeyColumns+`
		   from provider_keys pk
		   join key_pool_members m on m.key_id = pk.id
		  where m.pool_id = $1
		  order by pk.priority asc nulls last, pk.id asc`, poolID)
	if err != nil {
		return nil, err
	}
	out := make([]poolKeyRow, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, scanPoolKey(r))
	}
	return out, nil
}

// SetKeyStatus persists a KM-3 status transition (+ health for the probing pseudo-state). health
// "" writes SQL NULL.
func (st *pgStore) SetKeyStatus(ctx context.Context, keyID, status, health string) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`update provider_keys set status = $2, health = $3, updated_at = now() where id = $1`,
			keyID, status, nullText(health))
	})
}

// LeaseBatch is the guarded atomic lease (doc 07 §8). It ensures the budget row exists, rolls the
// day window over in place, then grants up to `batch` tokens without ever letting day_leased exceed
// dailyLimit. When the full batch would exceed the limit, it grants exactly the remainder.
func (st *pgStore) LeaseBatch(ctx context.Context, keyID string, batch int, dailyLimit int64) (int, error) {
	if dailyLimit <= 0 {
		return batch, nil // unlimited: no DB accounting needed
	}
	granted := 0
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(
			`insert into key_budgets (key_id, day, day_used, day_leased, month, month_used, month_leased)
			 values ($1, current_date, 0, 0, to_char(now(),'YYYY-MM'), 0, 0)
			 on conflict (key_id) do nothing`, keyID); err != nil {
			return err
		}
		if err := c.ExecParams(
			`update key_budgets set day = current_date, day_used = 0, day_leased = 0
			  where key_id = $1 and day < current_date`, keyID); err != nil {
			return err
		}
		res, err := c.QueryParams(
			`update key_budgets set day_leased = day_leased + $2::bigint, updated_at = now()
			  where key_id = $1 and day_leased + $2::bigint <= $3::bigint
			  returning day_leased`, keyID, int64(batch), dailyLimit)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			granted = batch
			return nil
		}
		// Full batch would exceed the limit: grant exactly the remainder up to daily_limit.
		cur, err := c.QueryParams(`select day_leased from key_budgets where key_id = $1`, keyID)
		if err != nil {
			return err
		}
		if len(cur.Rows) == 0 {
			return nil
		}
		remaining := dailyLimit - i64(cur.Rows[0][0])
		if remaining <= 0 {
			return nil
		}
		if int64(batch) < remaining {
			remaining = int64(batch)
		}
		res2, err := c.QueryParams(
			`update key_budgets set day_leased = day_leased + $2::bigint, updated_at = now()
			  where key_id = $1 and day_leased + $2::bigint <= $3::bigint
			  returning day_leased`, keyID, remaining, dailyLimit)
		if err != nil {
			return err
		}
		if len(res2.Rows) > 0 {
			granted = int(remaining)
		}
		return nil
	})
	return granted, err
}

// UsageDayTotals sums today's usage_events.credits by key_id. When usage_events is absent (pre-P4)
// it returns ErrUsageEventsAbsent so Reconcile is a documented no-op.
func (st *pgStore) UsageDayTotals(ctx context.Context) (map[string]int64, error) {
	totals := map[string]int64{}
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		reg, err := c.QueryParams(`select to_regclass('usage_events')`)
		if err != nil {
			return err
		}
		if len(reg.Rows) == 0 || reg.Rows[0][0] == nil {
			return ErrUsageEventsAbsent
		}
		res, err := c.QueryParams(
			`select key_id, coalesce(sum(credits),0) from usage_events
			  where created_at::date = current_date group by key_id`)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			totals[s(r[0])] = i64(r[1])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return totals, nil
}

// SetBudgetDayUsed rewrites key_budgets.day_used for each key to its ground-truth total.
func (st *pgStore) SetBudgetDayUsed(ctx context.Context, totals map[string]int64) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		for keyID, used := range totals {
			if err := c.ExecParams(
				`update key_budgets set day_used = $2::bigint, updated_at = now() where key_id = $1`,
				keyID, used); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- rotation_triggers ---

const triggerColumns = `trigger, coalesce(thresholds::text,''), cooldown_s, enabled, coalesce(updated_at::text,'')`

func scanTrigger(r []*string) TriggerRow {
	return TriggerRow{
		Trigger:    s(r[0]),
		Thresholds: s(r[1]),
		CooldownS:  pint(r[2]),
		Enabled:    s(r[3]) == "t" || s(r[3]) == "true",
		UpdatedAt:  s(r[4]),
	}
}

func (st *pgStore) ListTriggers(ctx context.Context) ([]TriggerRow, error) {
	var out []TriggerRow
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select ` + triggerColumns + ` from rotation_triggers order by trigger`)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			out = append(out, scanTrigger(r))
		}
		return nil
	})
	return out, err
}

func (st *pgStore) GetTrigger(ctx context.Context, name string) (TriggerRow, bool, error) {
	var tr TriggerRow
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select `+triggerColumns+` from rotation_triggers where trigger = $1`, name)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		tr = scanTrigger(res.Rows[0])
		found = true
		return nil
	})
	return tr, found, err
}

// UpsertTrigger writes a rotation_triggers row (last-write-wins single row, doc 04 §2.5).
func (st *pgStore) UpsertTrigger(ctx context.Context, tr TriggerRow, actor string) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into rotation_triggers (trigger, thresholds, cooldown_s, enabled, updated_at, updated_by)
			 values ($1, $2::jsonb, $3, $4, now(), $5::uuid)
			 on conflict (trigger) do update
			   set thresholds = excluded.thresholds, cooldown_s = excluded.cooldown_s,
			       enabled = excluded.enabled, updated_at = now(), updated_by = excluded.updated_by`,
			tr.Trigger, nullText(tr.Thresholds), nullIntCol(tr.CooldownS), tr.Enabled, nullText(actor))
	})
}

// --- small text-protocol column helpers (kept local so rotation stays self-contained) ---

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	return n
}

func pint(p *string) *int64 {
	if p == nil {
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func pfloat(p *string) *float64 {
	if p == nil {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(*p), 64)
	if err != nil {
		return nil
	}
	return &f
}

func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullIntCol(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// splitSelector parses provider_id:name (selector = provider_id||':'||name). provider ids are
// colon-free slugs, so a single split on the first ':' is exact.
func splitSelector(selector string) (providerID, name string, ok bool) {
	i := strings.IndexByte(selector, ':')
	if i <= 0 || i == len(selector)-1 {
		return "", "", false
	}
	return selector[:i], selector[i+1:], true
}
