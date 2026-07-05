package cost

import (
	"context"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// txRunner is the consumer-side slice of db.Store this package needs: a dual-GUC RLS transaction
// bound to the ctx Principal (G1). Satisfied by *db.Store.
type txRunner interface {
	Tx(ctx context.Context, fn func(*pg.Conn) error) error
}

var _ txRunner = (*db.Store)(nil)

// Service answers cost questions over the rollups. It holds no tenant state — every read binds the
// caller's Principal through store.Tx (G1); an injectable clock bounds retention windows.
type Service struct {
	store txRunner
	now   func() time.Time
}

// NewService builds a Service over store. now may be nil (wall clock).
func NewService(store txRunner, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// Row is one aggregated group-by bucket. The numerator (Credits) and its two denominators (Calls,
// Success) are carried TOGETHER — never a pre-divided ratio (doc 12 §P6) — so the HTTP layer can
// render credits_per_call / credits_per_successful_result without losing the underlying totals.
type Row struct {
	Key     string
	Credits int64
	Calls   int64
	Success int64
}

// Summary runs the whitelist group-by once (bounded page). isOperator gates group_by=key. It
// returns the aggregated rows ordered by group key and the next keyset cursor when more remain.
func (s *Service) Summary(ctx context.Context, groupBy string, from, to time.Time, filters map[string]string, isOperator bool, cur db.Cursor, limit int) ([]Row, db.Cursor, error) {
	q, err := buildQuery(groupBy, from, to, filters, isOperator, s.now())
	if err != nil {
		return nil, db.Cursor{}, err
	}
	limit = db.ClampLimit(limit)
	cursorKey := ""
	if len(cur.K) > 0 {
		cursorKey = cur.K[0]
	}
	rows, err := s.runGroup(ctx, q, cursorKey, limit+1)
	if err != nil {
		return nil, db.Cursor{}, err
	}
	var next db.Cursor
	if len(rows) > limit {
		rows = rows[:limit]
		next = db.Cursor{K: []string{rows[len(rows)-1].Key}}
	}
	return rows, next, nil
}

// runGroup executes one bound group-by query and scans the rows. Kept separate so the export
// streamer can reuse the IDENTICAL builder over its own short transactions (WYSIWYG, doc 04 §2.10).
func (s *Service) runGroup(ctx context.Context, q query, cursorKey string, limit int) ([]Row, error) {
	sql, args := q.sql(cursorKey, limit)
	var out []Row
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(sql, args...)
		if qerr != nil {
			return qerr
		}
		out = out[:0]
		for _, r := range res.Rows {
			out = append(out, Row{
				Key:     str(r[0]),
				Credits: i64(r[1]),
				Calls:   i64(r[2]),
				Success: i64(r[3]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ROIRow is cost-per-filled-Field for one (provider, workflow): credits over fields_filled, both
// carried so the ratio is derivable without a stored quotient (doc 04 §2.10).
type ROIRow struct {
	ProviderID   string
	WorkflowKey  string
	Credits      int64
	FieldsFilled int64
}

// ROI reads tenant_usage_1d (fields_filled) grouped by (provider, workflow) for the window. It is
// tenant-scoped by RLS; retention matches tenant_usage_1d (2y).
func (s *Service) ROI(ctx context.Context, from, to time.Time) ([]ROIRow, error) {
	if err := checkWindow(from, to, s.now(), 730*day); err != nil {
		return nil, err
	}
	var out []ROIRow
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select provider_id, workflow_key,
			        coalesce(sum(credits),0) as credits,
			        coalesce(sum(fields_filled),0) as fields
			   from tenant_usage_1d
			  where bucket_start >= $1 and bucket_start < $2
			  group by provider_id, workflow_key
			  order by provider_id asc, workflow_key asc`,
			from.UTC(), to.UTC())
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, ROIRow{
				ProviderID:   str(r[0]),
				WorkflowKey:  str(r[1]),
				Credits:      i64(r[2]),
				FieldsFilled: i64(r[3]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Forecast projects total tenant spend forward from a trailing 28-day daily series read from
// cost_rollup_1d. Below 14 days of history it returns method="insufficient_history" (forecast
// budget alerts stay disarmed). horizon<=0 defaults to a 7-day horizon.
func (s *Service) Forecast(ctx context.Context, horizon int) (Forecast, error) {
	now := s.now().UTC()
	// Trailing 28 days ending at today (UTC), zero-filled per calendar day.
	end := now.Truncate(day)
	start := end.Add(-28 * day)
	daily := make([]float64, 28)
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select day, coalesce(sum(credits),0) from cost_rollup_1d
			  where day >= $1 and day < $2 group by day`,
			start.Format("2006-01-02"), end.Format("2006-01-02"))
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			d := parseDay(str(r[0]))
			if d.IsZero() {
				continue
			}
			idx := int(d.Sub(start) / day)
			if idx >= 0 && idx < len(daily) {
				daily[idx] = float64(i64(r[1]))
			}
		}
		return nil
	})
	if err != nil {
		return Forecast{}, err
	}
	// history_days = number of days actually present (non-empty) in the trailing window.
	present := trimLeadingZeros(daily)
	return computeForecast(present, horizon), nil
}

// trimLeadingZeros drops leading all-zero days so history_days reflects the observed span (a Tenant
// that started 10 days ago has 10 days of history, not 28). Interior zeros are kept (real idle days).
func trimLeadingZeros(series []float64) []float64 {
	i := 0
	for i < len(series) && series[i] == 0 {
		i++
	}
	return series[i:]
}

// --- small column helpers (kept local so the package stays self-contained) ---

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*p, 10, 64)
	return n
}

func parseDay(s string) time.Time {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05-07"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Truncate(day)
		}
	}
	return time.Time{}
}
