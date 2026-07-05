package telemetry

import (
	"context"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// ErrWindowOutOfRange is returned when a read requests a window that is inverted or reaches
// beyond the resolution's retention (doc 03 §4). The HTTP layer maps it to 400
// window_out_of_range (doc 04 §1.4): the finer rollups do not retain the requested history.
var ErrWindowOutOfRange = errors.New("telemetry: requested window is out of range")

// providerStatsRetention is the doc 03 §4 retention per provider_stats resolution.
func providerStatsRetention(res Resolution) time.Duration {
	switch res {
	case Res1m:
		return 7 * day
	case Res1h:
		return 90 * day
	case Res1d:
		return 730 * day
	default:
		return 7 * day
	}
}

// keyUsageRetention is the doc 03 §4 retention per key_usage resolution.
func keyUsageRetention(res Resolution) time.Duration {
	switch res {
	case Res1m:
		return 3 * day
	case Res1h:
		return 30 * day
	case Res1d:
		return 365 * day
	default:
		return 3 * day
	}
}

// checkWindow rejects an inverted window or one whose lower bound predates the retention horizon
// (now - retention). It is a pure function so the bound logic is unit-testable without a DB.
func checkWindow(from, to, now time.Time, retention time.Duration) error {
	if !to.After(from) {
		return ErrWindowOutOfRange
	}
	if from.Before(now.Add(-retention)) {
		return ErrWindowOutOfRange
	}
	return nil
}

// ProviderStatRow is one folded provider_stats bucket (percentiles are computed at read from
// lat_hist, not stored — this returns the raw histogram + sum).
type ProviderStatRow struct {
	ProviderID  string
	BucketStart time.Time
	Req, OK     int64
	Fail        [8]int64
	Timeout     int64
	Credits     int64
	LatSumMs    int64
	LatHist     string // Postgres bigint[] text literal '{...}'
}

// ProviderStats returns provider_stats_<res> buckets for providerID in [from,to), ascending by
// bucket_start, bounded by db.ClampLimit(limit). It rejects windows beyond retention
// (ErrWindowOutOfRange). Reads run under the caller's Principal (Class P: an operator's
// platform-bound tx passes RLS; a customer Tenant fails closed).
func (a *Aggregator) ProviderStats(ctx context.Context, providerID string, res Resolution, from, to time.Time, limit int) ([]ProviderStatRow, error) {
	if err := checkWindow(from, to, a.now(), providerStatsRetention(res)); err != nil {
		return nil, err
	}
	table := "provider_stats_" + string(res)
	limit = db.ClampLimit(limit)
	var out []ProviderStatRow
	err := a.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select provider_id, bucket_start, req, ok, fail_auth, fail_rate_limit, fail_transient,
			        fail_not_found, fail_bad_request, fail_quota, fail_provider_down, fail_unknown,
			        timeout_count, credits_spent, lat_sum_ms, lat_hist
			   from `+table+`
			  where provider_id = $1 and bucket_start >= $2 and bucket_start < $3
			  order by bucket_start asc limit $4`,
			providerID, from, to, int64(limit))
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			row := ProviderStatRow{
				ProviderID:  s(r[0]),
				BucketStart: parseTS(s(r[1])),
				Req:         i64(r[2]),
				OK:          i64(r[3]),
				Timeout:     i64(r[12]),
				Credits:     i64(r[13]),
				LatSumMs:    i64(r[14]),
				LatHist:     s(r[15]),
			}
			for i := 0; i < 8; i++ {
				row.Fail[i] = i64(r[4+i])
			}
			out = append(out, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// KeyUsageRow is one folded key_usage bucket.
type KeyUsageRow struct {
	KeyID       string
	BucketStart time.Time
	Req, OK     int64
	Fail        int64
	Credits     int64
	LatHist     string
}

// KeyUsage returns key_usage_<res> buckets for keyID in [from,to), ascending, bounded by
// db.ClampLimit, retention-guarded. Class P: same RLS scoping as ProviderStats.
func (a *Aggregator) KeyUsage(ctx context.Context, keyID string, res Resolution, from, to time.Time, limit int) ([]KeyUsageRow, error) {
	if err := checkWindow(from, to, a.now(), keyUsageRetention(res)); err != nil {
		return nil, err
	}
	table := "key_usage_" + string(res)
	limit = db.ClampLimit(limit)
	var out []KeyUsageRow
	err := a.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select key_id, bucket_start, req, ok, fail, credits_spent, lat_hist
			   from `+table+`
			  where key_id = $1 and bucket_start >= $2 and bucket_start < $3
			  order by bucket_start asc limit $4`,
			keyID, from, to, int64(limit))
		if qerr != nil {
			return qerr
		}
		for _, r := range res.Rows {
			out = append(out, KeyUsageRow{
				KeyID:       s(r[0]),
				BucketStart: parseTS(s(r[1])),
				Req:         i64(r[2]),
				OK:          i64(r[3]),
				Fail:        i64(r[4]),
				Credits:     i64(r[5]),
				LatHist:     s(r[6]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
