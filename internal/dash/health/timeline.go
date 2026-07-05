package health

import "time"

// TimelineResult is the response of Timeline: a contiguous series of buckets (no gaps — every
// bucket in the window is present, empty ones as no_data) plus a window summary.
type TimelineResult struct {
	ProviderID  string    `json:"provider_id"`
	Granularity string    `json:"granularity"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Buckets     []Bucket  `json:"buckets"`
	Summary     Stats     `json:"summary"`
}

// buildDaySeries assembles a contiguous day-granularity series over [fromDay, toDay), filling
// each UTC day from the folded provider_health_1d rows (keyed by "2006-01-02"). Days with no row
// — or a row with zero checks — render StatusNoData (acceptance #4: never up). The series is
// capped at maxDayBuckets. Pure: no I/O, fully unit-testable.
func buildDaySeries(fromDay, toDay time.Time, rows map[string]DayRow) []Bucket {
	fromDay = truncDayUTC(fromDay)
	toDay = truncDayUTC(toDay)
	out := make([]Bucket, 0, maxDayBuckets)
	for d := fromDay; d.Before(toDay) && len(out) < maxDayBuckets; d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		r, ok := rows[key]
		if !ok || r.Checks <= 0 {
			out = append(out, Bucket{Start: d, Status: StatusNoData})
			continue
		}
		avg := 0.0
		if r.Checks > 0 {
			avg = float64(r.LatSumMS) / float64(r.Checks)
		}
		out = append(out, Bucket{
			Start:           d,
			Status:          dayBucketStatus(r),
			Checks:          r.Checks,
			UptimePct:       uptimePct(r.OK, r.Checks),
			AvgLatMS:        avg,
			WorstErrorClass: r.WorstErrorClass,
		})
	}
	return out
}

// buildHourSeries assembles a contiguous hour-granularity series over [from, to) from raw
// provider_health_checks aggregated per hour (keyed by the hour's unix seconds). Hours with no
// checks render StatusNoData. Capped at maxHourBuckets. Pure.
func buildHourSeries(from, to time.Time, rows map[int64]HourAgg) []Bucket {
	from = truncHourUTC(from)
	to = truncHourUTC(to)
	out := make([]Bucket, 0, maxHourBuckets)
	for h := from; h.Before(to) && len(out) < maxHourBuckets; h = h.Add(time.Hour) {
		a, ok := rows[h.Unix()]
		if !ok || a.Checks <= 0 {
			out = append(out, Bucket{Start: h, Status: StatusNoData})
			continue
		}
		avg := 0.0
		if a.Checks > 0 {
			avg = float64(a.LatSumMS) / float64(a.Checks)
		}
		out = append(out, Bucket{
			Start:     h,
			Status:    hourBucketStatus(a),
			Checks:    a.Checks,
			UptimePct: uptimePct(a.OK, a.Checks),
			AvgLatMS:  avg,
		})
	}
	return out
}

// truncDayUTC truncates t to the start of its UTC day.
func truncDayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// truncHourUTC truncates t to the start of its UTC hour.
func truncHourUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
}
