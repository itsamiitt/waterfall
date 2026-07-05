package telemetry

import (
	"testing"
	"time"
)

// TestBucketIndex pins the fixed 20-bucket log-spaced latency histogram (doc 03 §2.6): a latency
// lands in the first bucket whose upper bound it does not exceed, and the top bucket is a
// catch-all so every finite latency is counted exactly once.
func TestBucketIndex(t *testing.T) {
	cases := []struct {
		latMs int64
		want  int
	}{
		{0, 0},       // <= 1ms -> bucket 0
		{1, 0},       // boundary inclusive
		{2, 1},       // <= 2ms -> bucket 1
		{3, 2},       // > 2, <= 4
		{4, 2},       // boundary of bucket 2
		{64, 6},      // boundary
		{65, 7},      // just over 64 -> bucket 7 (<=125)
		{500, 9},     // boundary
		{1000, 10},   // boundary
		{256000, 18}, // boundary of the last finite bucket
		{256001, 19}, // just over -> top (catch-all) bucket
	}
	for _, c := range cases {
		if got := bucketIndex(c.latMs); got != c.want {
			t.Errorf("bucketIndex(%d) = %d, want %d", c.latMs, got, c.want)
		}
	}
	// Overflow: anything above the largest finite bound lands in the top (catch-all) bucket.
	if got := bucketIndex(1 << 40); got != histBuckets-1 {
		t.Errorf("bucketIndex(huge) = %d, want %d (top bucket)", got, histBuckets-1)
	}
	// The histogram is exactly 20 wide and its literal renders full width.
	var h [histBuckets]int64
	h[0], h[histBuckets-1] = 3, 7
	if lit := histLiteral(&h); lit != "{3,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,7}" {
		t.Errorf("histLiteral = %q", lit)
	}
	if histBuckets != 20 {
		t.Fatalf("histBuckets = %d, want 20 (doc 03 §2.6 fixed width)", histBuckets)
	}
}

// TestBucketStart checks UTC-aligned bucket truncation for the three resolutions.
func TestBucketStart(t *testing.T) {
	ts := time.Date(2026, 7, 2, 13, 47, 33, 500, time.UTC)
	if got := bucketStart(ts, Res1m); !got.Equal(time.Date(2026, 7, 2, 13, 47, 0, 0, time.UTC)) {
		t.Errorf("1m bucket = %v", got)
	}
	if got := bucketStart(ts, Res1h); !got.Equal(time.Date(2026, 7, 2, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("1h bucket = %v", got)
	}
	if got := bucketStart(ts, Res1d); !got.Equal(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("1d bucket = %v", got)
	}
}

// TestFailIndex maps the 8-class error taxonomy onto the provider_stats failure columns, with
// unknown classes folding into fail_unknown (fail-safe).
func TestFailIndex(t *testing.T) {
	want := map[string]int{
		"AUTH": 0, "RATE_LIMIT": 1, "TRANSIENT": 2, "NOT_FOUND": 3,
		"BAD_REQUEST": 4, "QUOTA": 5, "PROVIDER_DOWN": 6, "UNKNOWN": 7,
		"something-else": 7,
	}
	for oc, idx := range want {
		if got := failIndex(oc); got != idx {
			t.Errorf("failIndex(%q) = %d, want %d", oc, got, idx)
		}
	}
	if len(failCols) != 8 {
		t.Fatalf("failCols len = %d, want 8", len(failCols))
	}
}
