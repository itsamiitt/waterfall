package alerts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// doc10Path is the observability doc whose §4 table is the human-facing authority for the CLOSED
// alert-metric vocabulary. It is kept in lockstep with alerts.Metrics (OI-P6-1).
const doc10Path = "../../../docs/waterfall-dashboard/10-observability.md"

// metricRowRe matches a §4 table row's leading `| `+"`metric.name`"+` |` cell — the backticked
// metric identifier in the first column.
var metricRowRe = regexp.MustCompile("(?m)^\\|\\s*`([a-z][a-z0-9_.]+)`\\s*\\|")

// TestCatalogMatchesDoc10 is the OI-P6-1 lockstep gate: the set of metric names in doc 10 §4's
// CLOSED-vocabulary table must EXACTLY equal alerts.Metrics (17 entries incl. cost.anomaly). The
// companion overview.TestMetaEnumsParity pins /meta/enums == alerts.Metrics == 17, so together they
// give /meta/enums == alerts.Metrics == doc 10 §4.
func TestCatalogMatchesDoc10(t *testing.T) {
	raw, err := os.ReadFile(doc10Path)
	if err != nil {
		t.Fatalf("read doc 10: %v", err)
	}
	// The §4 table is the only place backticked `x.y` identifiers appear as the FIRST cell of a
	// row; restrict to the section between the "## 4." heading and the following "## 5." heading.
	text := string(raw)
	start := strings.Index(text, "## 4. Alert rule metric vocabulary")
	end := strings.Index(text, "## 5. Alert evaluation semantics")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not locate doc 10 §4 section (start=%d end=%d)", start, end)
	}
	section := text[start:end]

	docMetrics := map[string]bool{}
	for _, m := range metricRowRe.FindAllStringSubmatch(section, -1) {
		docMetrics[m[1]] = true
	}

	if len(docMetrics) != len(Metrics) {
		t.Fatalf("doc 10 §4 lists %d metrics, alerts.Metrics has %d — vocabulary drifted (OI-P6-1)",
			len(docMetrics), len(Metrics))
	}
	if len(Metrics) != 17 {
		t.Fatalf("alerts.Metrics = %d entries, want 17 (doc 10 §4 + cost.anomaly)", len(Metrics))
	}
	for _, def := range Metrics {
		if !docMetrics[def.Metric] {
			t.Errorf("metric %q is in alerts.Metrics but missing from doc 10 §4", def.Metric)
		}
		delete(docMetrics, def.Metric)
	}
	for extra := range docMetrics {
		t.Errorf("metric %q is in doc 10 §4 but not alerts.Metrics", extra)
	}
}
