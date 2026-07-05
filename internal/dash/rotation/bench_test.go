package rotation

import (
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/bandit"
	"github.com/enrichment/waterfall/internal/dash/keys"
)

// BenchmarkPoolSelect reports selections/s per pool for the hot-path selector (P2 acceptance #2,
// doc 12; design target >= 10,000/s, UNVERIFIED until P12). It measures the lock-free round_robin
// pick over a 20-key pool. `go test -bench=PoolSelect ./internal/dash/rotation/` prints the number.
func BenchmarkPoolSelect(b *testing.B) {
	rows := make([]poolKeyRow, 20)
	for i := range rows {
		rows[i] = poolKeyRow{
			ID:     string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Weight: 100,
			Status: keys.StatusActive,
		}
	}
	ps := buildPoolState("hunter:default", "round_robin", "", rows, bandit.New())

	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, ok := ps.Select(""); !ok {
			b.Fatal("Select returned no key")
		}
	}
	elapsed := time.Since(start)
	if elapsed > 0 {
		selPerSec := float64(b.N) / elapsed.Seconds()
		b.ReportMetric(selPerSec, "sel/s")
		b.Logf("round_robin: %.0f selections/s (target >= 10,000/s, UNVERIFIED until P12)", selPerSec)
	}
}

// BenchmarkPoolSelectWeighted reports selections/s for the alias-table weighted pick.
func BenchmarkPoolSelectWeighted(b *testing.B) {
	rows := make([]poolKeyRow, 20)
	for i := range rows {
		rows[i] = poolKeyRow{
			ID:     string(rune('a'+i%26)) + string(rune('0'+i/26)),
			Weight: 10 + i,
			Status: keys.StatusActive,
		}
	}
	ps := buildPoolState("hunter:default", "weighted", "", rows, bandit.New())

	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, ok := ps.Select(""); !ok {
			b.Fatal("Select returned no key")
		}
	}
	elapsed := time.Since(start)
	if elapsed > 0 {
		b.ReportMetric(float64(b.N)/elapsed.Seconds(), "sel/s")
	}
}
