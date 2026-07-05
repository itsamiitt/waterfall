package cost

import "math"

// Forecast is the projection returned by GET /v1/admin/cost/forecast (doc 04 §2.10). Below the
// minimum-history floor it reports method="insufficient_history" with empty arrays and no numeric
// projection, so forecast-budget alerts stay disarmed (doc 10 §5.2). At/above the floor it carries
// a per-horizon-day point projection with an ~80% indicative band whose math is UNVERIFIED until
// backtested (doc 12 §P6). All arrays are the same length (Horizon).
type Forecast struct {
	Method      string    `json:"method"`
	HistoryDays int       `json:"history_days"`
	Horizon     int       `json:"horizon,omitempty"`
	Point       []float64 `json:"point,omitempty"`
	Lower       []float64 `json:"lower,omitempty"`
	Upper       []float64 `json:"upper,omitempty"`
	Band        string    `json:"band,omitempty"` // "80% indicative" — the qualitative label
	Verified    bool      `json:"verified"`       // always false in v1 (UNVERIFIED, doc 12)
	Note        string    `json:"note,omitempty"`
}

const (
	// minHistoryDays is the doc-04 floor: below it we cannot fit a trend + weekly seasonality, so
	// the method degrades to insufficient_history and budget forecast alerts stay disarmed.
	minHistoryDays = 14
	// seasonPeriod is the multiplicative seasonality period (a 7-day week, doc 12 §P6).
	seasonPeriod = 7
	// z80 is the two-sided ~80% normal quantile (P10..P90); the band is point ± z80·residual_sd.
	z80 = 1.2816
)

// MethodInsufficient / MethodLinearSeasonal are the two method strings the SPA switches on.
const (
	MethodInsufficient   = "insufficient_history"
	MethodLinearSeasonal = "linear_seasonal"
)

// computeForecast projects the next horizon days from a trailing daily-credits series (ordered
// oldest→newest, one entry per calendar day, gaps already zero-filled). It is a pure function so
// the acceptance test (doc 13; P6 gate #3) needs no database:
//
//   - fewer than minHistoryDays points  -> method=insufficient_history, no projection.
//   - otherwise: least-squares line over the whole series + a 7-day multiplicative seasonal index
//     (mean ratio of actual/fitted per weekday, normalised to mean 1), extrapolated across the
//     horizon; the band is the residual standard deviation scaled to ~80% (z80).
func computeForecast(series []float64, horizon int) Forecast {
	n := len(series)
	if n < minHistoryDays {
		return Forecast{Method: MethodInsufficient, HistoryDays: n, Verified: false,
			Note: "need >= 14 days of history for a trend+seasonality projection"}
	}
	if horizon <= 0 {
		horizon = seasonPeriod
	}

	slope, intercept := leastSquares(series)
	seasonal := seasonalIndex(series, slope, intercept)
	resSD := residualSD(series, slope, intercept, seasonal)

	f := Forecast{
		Method: MethodLinearSeasonal, HistoryDays: n, Horizon: horizon,
		Point: make([]float64, horizon), Lower: make([]float64, horizon), Upper: make([]float64, horizon),
		Band: "80% indicative", Verified: false,
		Note: "band is indicative (~80%); projection math UNVERIFIED until backtested",
	}
	for h := 0; h < horizon; h++ {
		x := float64(n + h)
		trend := intercept + slope*x
		p := trend * seasonal[(n+h)%seasonPeriod]
		if p < 0 {
			p = 0
		}
		f.Point[h] = p
		lo := p - z80*resSD
		if lo < 0 {
			lo = 0
		}
		f.Lower[h] = lo
		f.Upper[h] = p + z80*resSD
	}
	return f
}

// leastSquares fits y = intercept + slope*x with x = 0..n-1 (ordinary least squares).
func leastSquares(y []float64) (slope, intercept float64) {
	n := float64(len(y))
	var sx, sy, sxx, sxy float64
	for i, v := range y {
		x := float64(i)
		sx += x
		sy += v
		sxx += x * x
		sxy += x * v
	}
	denom := n*sxx - sx*sx
	if denom == 0 {
		return 0, sy / n
	}
	slope = (n*sxy - sx*sy) / denom
	intercept = (sy - slope*sx) / n
	return slope, intercept
}

// seasonalIndex returns a 7-slot multiplicative index (mean actual/fitted per weekday slot,
// normalised so the slots average 1). A zero/negative fitted value contributes a neutral 1.
func seasonalIndex(y []float64, slope, intercept float64) [seasonPeriod]float64 {
	var sum [seasonPeriod]float64
	var cnt [seasonPeriod]int
	for i, v := range y {
		fitted := intercept + slope*float64(i)
		slot := i % seasonPeriod
		if fitted > 0 {
			sum[slot] += v / fitted
			cnt[slot]++
		}
	}
	var idx [seasonPeriod]float64
	var mean float64
	present := 0
	for s := 0; s < seasonPeriod; s++ {
		if cnt[s] > 0 {
			idx[s] = sum[s] / float64(cnt[s])
			mean += idx[s]
			present++
		} else {
			idx[s] = 1
		}
	}
	if present == 0 {
		for s := range idx {
			idx[s] = 1
		}
		return idx
	}
	mean /= float64(present)
	if mean == 0 {
		return idx
	}
	for s := 0; s < seasonPeriod; s++ {
		if cnt[s] > 0 {
			idx[s] /= mean
		}
	}
	return idx
}

// residualSD is the standard deviation of (actual - trend*seasonal) across the fitted series; it
// scales the indicative band.
func residualSD(y []float64, slope, intercept float64, seasonal [seasonPeriod]float64) float64 {
	n := len(y)
	if n < 2 {
		return 0
	}
	var ss float64
	for i, v := range y {
		fit := (intercept + slope*float64(i)) * seasonal[i%seasonPeriod]
		d := v - fit
		ss += d * d
	}
	return math.Sqrt(ss / float64(n-1))
}
