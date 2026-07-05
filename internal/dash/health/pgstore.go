package health

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// PGStore is the production Store over the Class-P health tables (provider_health_checks,
// provider_health_1d — migration 0009) and health_schedules (migration 0005). Every method runs
// under db.Store.PlatformTx (tenant='platform'), the sanctioned system path for platform_only
// tables — the scheduler and reactivator carry no request Principal.
type PGStore struct {
	store *db.Store
}

// NewPGStore wires a PGStore to the shared db.Store.
func NewPGStore(store *db.Store) *PGStore { return &PGStore{store: store} }

var (
	_ Store               = (*PGStore)(nil)
	_ KeyReactivatorStore = (*PGStore)(nil)
)

// WriteCheck inserts one raw provider_health_checks row. Only status/latency/class are recorded —
// never any secret or payload (doc 10 §3). key_id/region/http_status/error_class NULL out when unset.
func (s *PGStore) WriteCheck(ctx context.Context, providerID string, r CheckResult, at time.Time) error {
	return s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into provider_health_checks
			   (provider_id, key_id, region, checked_at, status, http_status, lat_ms, error_class)
			 values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			providerID,
			nilStr(r.KeyID),
			nilStr(r.Region),
			at.UTC(),
			r.Status,
			nilZeroInt(r.HTTPStatus),
			int64(r.LatencyMS),
			nilStr(r.ErrorClass),
		)
	})
}

// FoldDay recomputes provider_health_1d for one UTC day from raw checks and REPLACES the bucket
// (repair-refold semantics, doc 03 §9.4). worst_error_class is the most-severe class seen that day
// (PROVIDER_DOWN worst ... UNKNOWN least; NULL when the day had no errors). Returns providers folded.
func (s *PGStore) FoldDay(ctx context.Context, dayUTC time.Time) (int, error) {
	day := truncDayUTC(dayUTC)
	dayStr := day.Format("2006-01-02")
	start := day
	end := day.AddDate(0, 0, 1)
	n := 0
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`insert into provider_health_1d
			   (provider_id, day, checks, ok, degraded, down, lat_sum_ms, worst_error_class)
			 select provider_id,
			   $1::date,
			   count(*),
			   count(*) filter (where status = 'up'),
			   count(*) filter (where status = 'degraded'),
			   count(*) filter (where status = 'down'),
			   coalesce(sum(lat_ms), 0),
			   case min(case error_class
			       when 'PROVIDER_DOWN' then 1 when 'AUTH' then 2 when 'QUOTA' then 3
			       when 'RATE_LIMIT' then 4 when 'TRANSIENT' then 5 when 'BAD_REQUEST' then 6
			       when 'NOT_FOUND' then 7 when 'UNKNOWN' then 8 else 99 end)
			     when 1 then 'PROVIDER_DOWN' when 2 then 'AUTH' when 3 then 'QUOTA'
			     when 4 then 'RATE_LIMIT' when 5 then 'TRANSIENT' when 6 then 'BAD_REQUEST'
			     when 7 then 'NOT_FOUND' when 8 then 'UNKNOWN' else null end
			 from provider_health_checks
			 where checked_at >= $2 and checked_at < $3
			 group by provider_id
			 on conflict (provider_id, day) do update set
			   checks = excluded.checks, ok = excluded.ok, degraded = excluded.degraded,
			   down = excluded.down, lat_sum_ms = excluded.lat_sum_ms,
			   worst_error_class = excluded.worst_error_class
			 returning provider_id`,
			dayStr, start, end)
		if err != nil {
			return err
		}
		n = len(res.Rows)
		return nil
	})
	return n, err
}

// DayBuckets returns folded rows keyed by "2006-01-02" for [fromDay, toDay).
func (s *PGStore) DayBuckets(ctx context.Context, providerID string, fromDay, toDay time.Time) (map[string]DayRow, error) {
	out := map[string]DayRow{}
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select to_char(day,'YYYY-MM-DD'), checks, ok, degraded, down, lat_sum_ms,
			        coalesce(worst_error_class,'')
			 from provider_health_1d
			 where provider_id = $1 and day >= $2::date and day < $3::date`,
			providerID, fromDay.Format("2006-01-02"), toDay.Format("2006-01-02"))
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			key := pStr(row[0])
			out[key] = DayRow{
				Day:             pDate(key),
				Checks:          pInt64(row[1]),
				OK:              pInt64(row[2]),
				Degraded:        pInt64(row[3]),
				Down:            pInt64(row[4]),
				LatSumMS:        pInt64(row[5]),
				WorstErrorClass: pStr(row[6]),
			}
		}
		return nil
	})
	return out, err
}

// HourBuckets aggregates raw checks into UTC-hour buckets keyed by unix seconds for [from, to).
func (s *PGStore) HourBuckets(ctx context.Context, providerID string, from, to time.Time) (map[int64]HourAgg, error) {
	out := map[int64]HourAgg{}
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select to_char(date_trunc('hour', checked_at at time zone 'UTC'),'YYYY-MM-DD HH24:MI:SS'),
			        count(*), count(*) filter (where status='up'),
			        count(*) filter (where status='degraded'), count(*) filter (where status='down'),
			        coalesce(sum(lat_ms),0)
			 from provider_health_checks
			 where provider_id = $1 and checked_at >= $2 and checked_at < $3
			 group by 1`,
			providerID, from.UTC(), to.UTC())
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			h := pClockUTC(pStr(row[0]))
			out[h.Unix()] = HourAgg{
				Hour:     h,
				Checks:   pInt64(row[1]),
				OK:       pInt64(row[2]),
				Degraded: pInt64(row[3]),
				Down:     pInt64(row[4]),
				LatSumMS: pInt64(row[5]),
			}
		}
		return nil
	})
	return out, err
}

// SampleWindow returns the ascending latency sample plus outcome counts for [from, to). The sample
// is bounded by sampleCap so the read stays bounded regardless of window size.
func (s *PGStore) SampleWindow(ctx context.Context, providerID string, from, to time.Time) (WindowSample, error) {
	var ws WindowSample
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		cnt, err := c.QueryParams(
			`select count(*), count(*) filter (where status='up')
			 from provider_health_checks
			 where provider_id = $1 and checked_at >= $2 and checked_at < $3`,
			providerID, from.UTC(), to.UTC())
		if err != nil {
			return err
		}
		if len(cnt.Rows) > 0 {
			ws.Checks = pInt64(cnt.Rows[0][0])
			ws.OK = pInt64(cnt.Rows[0][1])
		}
		lat, err := c.QueryParams(
			`select lat_ms from provider_health_checks
			 where provider_id = $1 and checked_at >= $2 and checked_at < $3 and lat_ms is not null
			 order by lat_ms asc limit $4`,
			providerID, from.UTC(), to.UTC(), int64(sampleCap))
		if err != nil {
			return err
		}
		ws.Lats = make([]int, 0, len(lat.Rows))
		for _, row := range lat.Rows {
			ws.Lats = append(ws.Lats, int(pInt64(row[0])))
		}
		return nil
	})
	return ws, err
}

// ProviderStatuses returns, per Provider checked in the last 48h, its latest status and 24h uptime.
func (s *PGStore) ProviderStatuses(ctx context.Context) ([]ProviderStatus, error) {
	var out []ProviderStatus
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select provider_id,
			        (array_agg(status order by checked_at desc))[1],
			        max(checked_at),
			        coalesce((array_agg(lat_ms order by checked_at desc))[1], 0),
			        coalesce((array_agg(error_class order by checked_at desc) filter (where error_class is not null))[1], ''),
			        count(*) filter (where checked_at >= now() - interval '24 hours'),
			        count(*) filter (where checked_at >= now() - interval '24 hours' and status='up')
			 from provider_health_checks
			 where checked_at >= now() - interval '48 hours'
			 group by provider_id
			 order by provider_id`,
		)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			checks24 := pInt64(row[5])
			ps := ProviderStatus{
				ProviderID:   pStr(row[0]),
				Status:       pStr(row[1]),
				LastCheckAt:  pTimePtr(row[2]),
				LastLatMS:    int(pInt64(row[3])),
				ErrorClass:   pStr(row[4]),
				Checks24h:    checks24,
				UptimePct24h: uptimePct(pInt64(row[6]), checks24),
			}
			out = append(out, ps)
		}
		return nil
	})
	return out, err
}

// Regional aggregates health by region over [from, to).
func (s *PGStore) Regional(ctx context.Context, from, to time.Time) ([]RegionAgg, error) {
	var out []RegionAgg
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select coalesce(nullif(region,''),'unknown'), count(*),
			        count(*) filter (where status='up'), count(*) filter (where status='degraded'),
			        count(*) filter (where status='down')
			 from provider_health_checks
			 where checked_at >= $1 and checked_at < $2
			 group by 1 order by 1`,
			from.UTC(), to.UTC())
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			checks := pInt64(row[1])
			ok := pInt64(row[2])
			out = append(out, RegionAgg{
				Region:    pStr(row[0]),
				Checks:    checks,
				OK:        ok,
				Degraded:  pInt64(row[3]),
				Down:      pInt64(row[4]),
				UptimePct: uptimePct(ok, checks),
			})
		}
		return nil
	})
	return out, err
}

// ListCheckTargets returns the scheduler's per-Provider check targets: the providers catalog
// connection descriptor LEFT JOINed with health_schedules (missing schedule => defaults). Only
// Providers with a base_url and an effectively-enabled schedule are returned.
func (s *PGStore) ListCheckTargets(ctx context.Context) ([]Target, error) {
	var out []Target
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select p.id, coalesce(p.base_url,''), coalesce(p.auth_scheme,''),
			        coalesce(p.auth_header,''), coalesce(p.auth_query_param,''),
			        coalesce(p.timeout_ms,0), coalesce(p.breaker_threshold,0),
			        coalesce(p.breaker_cooldown_s,0), p.region,
			        coalesce(hs.interval_s,60), coalesce(hs.jitter_pct,10), coalesce(hs.enabled,true)
			 from providers p
			 left join health_schedules hs on hs.provider_id = p.id
			 where p.base_url is not null and p.base_url <> ''
			   and coalesce(hs.enabled, true) = true
			 order by p.id`,
		)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, scanTarget(row))
		}
		return nil
	})
	return out, err
}

// ProviderTarget returns the check target for one Provider (ad-hoc POST /health/checks/run).
func (s *PGStore) ProviderTarget(ctx context.Context, providerID string) (Target, bool, error) {
	var t Target
	found := false
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select p.id, coalesce(p.base_url,''), coalesce(p.auth_scheme,''),
			        coalesce(p.auth_header,''), coalesce(p.auth_query_param,''),
			        coalesce(p.timeout_ms,0), coalesce(p.breaker_threshold,0),
			        coalesce(p.breaker_cooldown_s,0), p.region,
			        coalesce(hs.interval_s,60), coalesce(hs.jitter_pct,10), coalesce(hs.enabled,true)
			 from providers p
			 left join health_schedules hs on hs.provider_id = p.id
			 where p.id = $1`,
			providerID)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			t = scanTarget(res.Rows[0])
			found = true
		}
		return nil
	})
	return t, found, err
}

// ListSchedules returns every health_schedules row.
func (s *PGStore) ListSchedules(ctx context.Context) ([]Schedule, error) {
	var out []Schedule
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select provider_id, interval_s, jitter_pct, regions, enabled, updated_at
			 from health_schedules order by provider_id`,
		)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, Schedule{
				ProviderID: pStr(row[0]),
				IntervalS:  int(pInt64(row[1])),
				JitterPct:  int(pInt64(row[2])),
				Regions:    parseTextArray(row[3]),
				Enabled:    pBool(row[4]),
				UpdatedAt:  pTime(row[5]),
			})
		}
		return nil
	})
	return out, err
}

// UpsertSchedule persists one Provider's schedule and returns the stored row.
func (s *PGStore) UpsertSchedule(ctx context.Context, sc Schedule) (Schedule, error) {
	var out Schedule
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`insert into health_schedules (provider_id, interval_s, jitter_pct, regions, enabled, updated_at, updated_by)
			 values ($1, $2, $3, $4::text[], $5, now(), $6)
			 on conflict (provider_id) do update set
			   interval_s = excluded.interval_s, jitter_pct = excluded.jitter_pct,
			   regions = excluded.regions, enabled = excluded.enabled,
			   updated_at = now(), updated_by = excluded.updated_by
			 returning provider_id, interval_s, jitter_pct, regions, enabled, updated_at`,
			sc.ProviderID, int64(sc.IntervalS), int64(sc.JitterPct),
			formatTextArray(sc.Regions), sc.Enabled, nilStr(sc.UpdatedBy))
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			row := res.Rows[0]
			out = Schedule{
				ProviderID: pStr(row[0]),
				IntervalS:  int(pInt64(row[1])),
				JitterPct:  int(pInt64(row[2])),
				Regions:    parseTextArray(row[3]),
				Enabled:    pBool(row[4]),
				UpdatedAt:  pTime(row[5]),
			}
		}
		return nil
	})
	return out, err
}

// ExhaustedKeys returns up to limit Provider Key ids in status exhausted/rate_limited (auto
// re-enable candidates), oldest failure first. Reads provider_keys (Class P) via PlatformTx.
func (s *PGStore) ExhaustedKeys(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultReactivateBatch
	}
	var out []string
	err := s.store.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select id::text from provider_keys
			 where status in ('exhausted','rate_limited')
			 order by last_failure_at asc nulls last limit $1`,
			int64(limit))
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, pStr(row[0]))
		}
		return nil
	})
	return out, err
}

// scanTarget maps a target-projection row into a Target.
func scanTarget(row []*string) Target {
	return Target{
		ProviderID:       pStr(row[0]),
		BaseURL:          pStr(row[1]),
		AuthScheme:       pStr(row[2]),
		AuthHeader:       pStr(row[3]),
		AuthQueryParam:   pStr(row[4]),
		TimeoutMS:        int(pInt64(row[5])),
		BreakerThreshold: int(pInt64(row[6])),
		BreakerCooldownS: int(pInt64(row[7])),
		Regions:          parseTextArray(row[8]),
		IntervalS:        int(pInt64(row[9])),
		JitterPct:        int(pInt64(row[10])),
		Enabled:          pBool(row[11]),
	}
}

// --- pg text-protocol decode helpers (nil-safe) ---

func nilStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilZeroInt(n int) any {
	if n == 0 {
		return nil
	}
	return int64(n)
}

func pStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func pInt64(p *string) int64 {
	if p == nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func pBool(p *string) bool {
	return p != nil && (*p == "t" || *p == "true")
}

func pTime(p *string) time.Time {
	if p == nil {
		return time.Time{}
	}
	return parseTS(*p)
}

func pTimePtr(p *string) *time.Time {
	if p == nil || *p == "" {
		return nil
	}
	t := parseTS(*p)
	if t.IsZero() {
		return nil
	}
	return &t
}

// pDate parses a "YYYY-MM-DD" date string into a UTC midnight time.Time.
func pDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// pClockUTC parses a "YYYY-MM-DD HH:MM:SS" wall-clock string (from date_trunc ... at time zone
// 'UTC') as a UTC instant.
func pClockUTC(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseTS parses a Postgres timestamptz text rendering (or RFC3339) into a time.Time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// parseTextArray parses a Postgres text[] literal like {a,b,"c d"} into a []string ({}/NULL => nil).
func parseTextArray(p *string) []string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote, esc := false, false
	flush := func() { out = append(out, cur.String()); cur.Reset() }
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			cur.WriteByte(ch)
			esc = false
		case ch == '\\':
			esc = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ',' && !inQuote:
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	for i, v := range out {
		if v == "NULL" {
			out[i] = ""
		}
	}
	return out
}

// formatTextArray renders elements as a Postgres array literal ({"a","b"}); nil/empty => "{}".
func formatTextArray(elems []string) string {
	if len(elems) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(e))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
