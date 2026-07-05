package providers

import "sort"

// opStateAction maps a lifecycle action verb to the op_state it targets.
var opStateAction = map[string]string{
	"enable":      OpEnabled,
	"disable":     OpDisabled,
	"pause":       OpPaused,
	"maintenance": OpMaintenance,
}

// opStateTransitions is the valid-transition guard: from-state -> allowed to-states. Every real
// transition changes state; re-issuing the current state is rejected (idempotent no-op is not a
// transition) so the audit trail records only genuine state changes.
var opStateTransitions = map[string]map[string]bool{
	OpEnabled:     {OpDisabled: true, OpPaused: true, OpMaintenance: true},
	OpDisabled:    {OpEnabled: true, OpPaused: true, OpMaintenance: true},
	OpPaused:      {OpEnabled: true, OpDisabled: true, OpMaintenance: true},
	OpMaintenance: {OpEnabled: true, OpDisabled: true, OpPaused: true},
}

// canTransition reports whether op_state may move from -> to.
func canTransition(from, to string) bool {
	if from == to {
		return false
	}
	return opStateTransitions[from][to]
}

// --- rankings (doc 04 §2.3 GET /providers/rankings) ---

// Ranking is one provider's position under a scoring metric.
type Ranking struct {
	Rank        int      `json:"rank"`
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Score       *float64 `json:"score"`
}

// rankingMetric selects a score column and its ordering direction.
type rankingMetric struct {
	extract func(Provider) *float64
	asc     bool // true => smaller is better (cost)
}

var rankingMetrics = map[string]rankingMetric{
	"health_score":      {func(p Provider) *float64 { return p.HealthScore }, false},
	"success_score":     {func(p Provider) *float64 { return p.SuccessScore }, false},
	"confidence_score":  {func(p Provider) *float64 { return p.ConfidenceScore }, false},
	"performance_score": {func(p Provider) *float64 { return p.PerformanceScore }, false},
	"cost_score":        {func(p Provider) *float64 { return p.CostScore }, true},
}

// rankBy orders providers by the named metric (default health_score). Providers with a NULL
// score always sort last regardless of direction. Ties break by id for determinism.
func rankBy(providers []Provider, metric string) []Ranking {
	m, ok := rankingMetrics[metric]
	if !ok {
		m = rankingMetrics["health_score"]
	}
	ranked := make([]Provider, len(providers))
	copy(ranked, providers)
	sort.SliceStable(ranked, func(i, j int) bool {
		si, sj := m.extract(ranked[i]), m.extract(ranked[j])
		switch {
		case si == nil && sj == nil:
			return ranked[i].ID < ranked[j].ID
		case si == nil:
			return false // nil sorts last
		case sj == nil:
			return true
		case *si == *sj:
			return ranked[i].ID < ranked[j].ID
		case m.asc:
			return *si < *sj
		default:
			return *si > *sj
		}
	})
	out := make([]Ranking, 0, len(ranked))
	for i, p := range ranked {
		out = append(out, Ranking{Rank: i + 1, ID: p.ID, DisplayName: p.DisplayName, Score: m.extract(p)})
	}
	return out
}

// --- coverage (doc 04 §2.3 GET /providers/coverage) ---

// coverageGroup names a family of canonical Fields (domain vocabulary). The catalog has no
// dedicated "intent" Field, so signal/firmographic capabilities are aggregated under
// "firmographic"; email and phone map directly.
type coverageGroup struct {
	name   string
	fields map[string]bool
}

var coverageGroups = []coverageGroup{
	{"email", set("work_email", "personal_email", "email_status")},
	{"phone", set("mobile_phone", "direct_dial", "office_phone", "phone_status")},
	{"firmographic", set(
		"company_domain", "company_name", "employee_count", "industry",
		"linkedin_url", "job_title", "seniority", "department")},
}

// GroupCoverage is one field-group's coverage across the catalog.
type GroupCoverage struct {
	ProvidersCovering int     `json:"providers_covering"`
	Pct               float64 `json:"pct"`
}

// CoverageReport is the aggregate declared-capability coverage over the (non-archived) catalog.
type CoverageReport struct {
	Total  int                      `json:"total_providers"`
	Groups map[string]GroupCoverage `json:"groups"`
	Grid   map[string][]string      `json:"grid"` // field -> provider ids declaring it
}

// coverage aggregates declared capabilities into per-group percentages and a field×provider grid.
// Archived providers are excluded from both the denominator and the grid.
func coverage(providers []Provider) CoverageReport {
	rep := CoverageReport{Groups: map[string]GroupCoverage{}, Grid: map[string][]string{}}
	live := make([]Provider, 0, len(providers))
	for _, p := range providers {
		if p.ArchivedAt != nil {
			continue
		}
		live = append(live, p)
	}
	rep.Total = len(live)

	for _, g := range coverageGroups {
		covering := 0
		for _, p := range live {
			if declaresAny(p, g.fields) {
				covering++
			}
		}
		pct := 0.0
		if rep.Total > 0 {
			pct = float64(covering) / float64(rep.Total) * 100
		}
		rep.Groups[g.name] = GroupCoverage{ProvidersCovering: covering, Pct: pct}
	}

	for _, p := range live {
		for _, c := range p.Capabilities {
			rep.Grid[c.Field] = append(rep.Grid[c.Field], p.ID)
		}
	}
	for f := range rep.Grid {
		sort.Strings(rep.Grid[f])
	}
	return rep
}

func declaresAny(p Provider, fields map[string]bool) bool {
	for _, c := range p.Capabilities {
		if fields[c.Field] {
			return true
		}
	}
	return false
}

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// --- compare (doc 04 §2.3 GET /providers/compare) ---

// CompareEntry is one provider's side-by-side declared capabilities and measured provider-level
// scores. Per-Field measured metrics require provider_stats_* rollups (doc 10) and are deferred;
// P1 compares declared capabilities against provider-level measured scores.
type CompareEntry struct {
	ID                 string       `json:"id"`
	DisplayName        string       `json:"display_name"`
	Status             string       `json:"status"`
	OpState            string       `json:"op_state"`
	EffectiveAvailable bool         `json:"effective_available"`
	Availability       string       `json:"availability"`
	Declared           []Capability `json:"declared_capabilities"`
	HealthScore        *float64     `json:"health_score"`
	CostScore          *float64     `json:"cost_score"`
	SuccessScore       *float64     `json:"success_score"`
	ConfidenceScore    *float64     `json:"confidence_score"`
	AvgLatencyMS       *float64     `json:"avg_latency_ms"`
	CreditsRemaining   *int64       `json:"credits_remaining"`
}

// compareEntries projects providers into comparison rows (order preserved from the input).
func compareEntries(providers []Provider) []CompareEntry {
	out := make([]CompareEntry, 0, len(providers))
	for _, p := range providers {
		av := EffectiveAvailability(p)
		out = append(out, CompareEntry{
			ID:                 p.ID,
			DisplayName:        p.DisplayName,
			Status:             p.Status,
			OpState:            p.OpState,
			EffectiveAvailable: av.Available(),
			Availability:       string(av.State),
			Declared:           p.Capabilities,
			HealthScore:        p.HealthScore,
			CostScore:          p.CostScore,
			SuccessScore:       p.SuccessScore,
			ConfidenceScore:    p.ConfidenceScore,
			AvgLatencyMS:       p.AvgLatencyMS,
			CreditsRemaining:   p.CreditsRemaining,
		})
	}
	return out
}
