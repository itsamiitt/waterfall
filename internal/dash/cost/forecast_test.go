package cost

import "testing"

// TestForecastInsufficientHistory is P6 gate #3 (first half): below 14 days of history the method is
// insufficient_history with no numeric projection, so forecast-budget alerts stay disarmed.
func TestForecastInsufficientHistory(t *testing.T) {
	for _, n := range []int{0, 1, 7, 13} {
		series := make([]float64, n)
		for i := range series {
			series[i] = 100 + float64(i)
		}
		f := computeForecast(series, 7)
		if f.Method != MethodInsufficient {
			t.Fatalf("n=%d: method=%q, want %q", n, f.Method, MethodInsufficient)
		}
		if f.HistoryDays != n {
			t.Fatalf("n=%d: history_days=%d, want %d", n, f.HistoryDays, n)
		}
		if len(f.Point) != 0 || len(f.Lower) != 0 || len(f.Upper) != 0 {
			t.Fatalf("n=%d: insufficient_history must carry no projection arrays", n)
		}
	}
}

// TestForecastProjection is P6 gate #3 (second half): >= 14 days yields a numeric projection with a
// band (point/lower/upper same length as the horizon, ordered lower<=point<=upper), tagged
// UNVERIFIED.
func TestForecastProjection(t *testing.T) {
	// 28 days: a rising linear trend with a mild weekly ripple.
	series := make([]float64, 28)
	for i := range series {
		base := 1000 + 50*float64(i)
		weekly := 1.0 + 0.1*float64((i%7)-3)/3.0
		series[i] = base * weekly
	}
	horizon := 7
	f := computeForecast(series, horizon)
	if f.Method != MethodLinearSeasonal {
		t.Fatalf("method=%q, want %q", f.Method, MethodLinearSeasonal)
	}
	if f.HistoryDays != 28 {
		t.Fatalf("history_days=%d, want 28", f.HistoryDays)
	}
	if f.Verified {
		t.Fatalf("forecast must be tagged UNVERIFIED in v1")
	}
	if len(f.Point) != horizon || len(f.Lower) != horizon || len(f.Upper) != horizon {
		t.Fatalf("arrays len = %d/%d/%d, want %d each", len(f.Point), len(f.Lower), len(f.Upper), horizon)
	}
	for i := 0; i < horizon; i++ {
		if !(f.Lower[i] <= f.Point[i] && f.Point[i] <= f.Upper[i]) {
			t.Fatalf("h=%d: band not ordered: lower=%g point=%g upper=%g", i, f.Lower[i], f.Point[i], f.Upper[i])
		}
		if f.Point[i] <= 0 {
			t.Fatalf("h=%d: projected point should be positive on a rising series, got %g", i, f.Point[i])
		}
	}
	// Trend is clearly upward: the projected first day should exceed the series mean.
	var sum float64
	for _, v := range series {
		sum += v
	}
	if f.Point[0] < sum/float64(len(series)) {
		t.Fatalf("projection should extrapolate the upward trend; point[0]=%g mean=%g", f.Point[0], sum/float64(len(series)))
	}
}

// TestLeastSquaresLine sanity-checks the OLS fit on a pure line y = 3 + 2x.
func TestLeastSquaresLine(t *testing.T) {
	y := make([]float64, 20)
	for i := range y {
		y[i] = 3 + 2*float64(i)
	}
	slope, intercept := leastSquares(y)
	if abs(slope-2) > 1e-9 || abs(intercept-3) > 1e-9 {
		t.Fatalf("slope=%g intercept=%g, want 2 and 3", slope, intercept)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
