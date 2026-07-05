package routing

import (
	"testing"

	"github.com/enrichment/waterfall/internal/dash/configver"
)

// TestResolve_PrecedenceTable exhaustively proves the doc 07 §3.2 most-specific-wins lattice: for a
// setting defined at every level, level 1 wins; removing the winner promotes the next level, walking
// all 8 levels as the effective source (acceptance criterion #4).
func TestResolve_PrecedenceTable_AllEightLevels(t *testing.T) {
	// Each level defines confidence_target = its level number (as a float), so the winner's value
	// identifies the source level unambiguously.
	full := map[ScopeLevel]Policy{}
	for _, lvl := range levelOrder {
		v := float64(lvl)
		full[lvl] = Policy{Thresholds: Thresholds{ConfidenceTarget: &v}}
	}

	dims := configver.ScopeDims{Product: "prospector", Country: "DE"}
	// Peel levels off the front (most specific) and assert the next-most-specific present wins.
	for i := range levelOrder {
		active := map[ScopeLevel]Policy{}
		for _, lvl := range levelOrder[i:] {
			active[lvl] = full[lvl]
		}
		want := levelOrder[i]
		eff := Resolve(dims, active)
		if eff.ConfidenceTarget == nil {
			t.Fatalf("level %d present but confidence_target unresolved", want)
		}
		if eff.ConfidenceTarget.Source != want {
			t.Fatalf("winner source = %v, want %v (present: %v)", eff.ConfidenceTarget.Source, want, levelOrder[i:])
		}
		if eff.ConfidenceTarget.Value != float64(want) {
			t.Fatalf("winner value = %v, want %v", eff.ConfidenceTarget.Value, float64(want))
		}
	}
}

// TestResolve_SettingDefinedOnlyAtLevel8 proves platform-default fallthrough carries its source.
func TestResolve_SettingDefinedOnlyAtLevel8(t *testing.T) {
	v := 0.42
	active := map[ScopeLevel]Policy{
		LevelPlatformDefault: {Thresholds: Thresholds{ConfidenceTarget: &v}},
	}
	eff := Resolve(configver.ScopeDims{}, active)
	if eff.ConfidenceTarget == nil || eff.ConfidenceTarget.Source != LevelPlatformDefault {
		t.Fatalf("expected source=platform default, got %+v", eff.ConfidenceTarget)
	}
	if eff.ConfidenceTarget.Source.String() != "default" {
		t.Fatalf("level 8 label = %q, want default", eff.ConfidenceTarget.Source.String())
	}
}

// TestResolve_UndefinedSetting proves a setting defined nowhere resolves to nil (engine default).
func TestResolve_UndefinedSetting(t *testing.T) {
	eff := Resolve(configver.ScopeDims{}, map[ScopeLevel]Policy{
		LevelTenantDefault: {Thresholds: Thresholds{}}, // present but defines nothing
	})
	if eff.MinConfidence != nil {
		t.Fatalf("undefined min_confidence should be nil, got %+v", eff.MinConfidence)
	}
	if ScopeLevel(99).String() != "engine_default" {
		t.Fatalf("unknown level label = %q, want engine_default", ScopeLevel(99).String())
	}
}

// TestResolve_TriStateFold reproduces the doc 07 §3.3 worked example: provider hunter is inherit at
// level 3, off at level 4, on at level 8 -> effective off, source = tenant scope (level 4). inherit
// is transparent.
func TestResolve_TriStateFold_InheritTransparent(t *testing.T) {
	active := map[ScopeLevel]Policy{
		LevelTenantCountry:   {ProviderOverrides: map[string]Override{"hunter": {Mode: "inherit"}}},
		LevelTenantDefault:   {ProviderOverrides: map[string]Override{"hunter": {Mode: "off"}}},
		LevelPlatformDefault: {ProviderOverrides: map[string]Override{"hunter": {Mode: "on"}}},
	}
	eff := Resolve(configver.ScopeDims{Country: "DE"}, active)
	got, ok := eff.ProviderModes["hunter"]
	if !ok {
		t.Fatal("hunter override unresolved")
	}
	if got.Mode != "off" {
		t.Fatalf("effective mode = %q, want off (inherit at level 3 must be transparent)", got.Mode)
	}
	if got.Source != LevelTenantDefault {
		t.Fatalf("effective source = %v, want tenant default (level 4)", got.Source)
	}
}

// TestResolve_ProviderModeMostSpecificWins proves a non-inherit at a more specific level wins and
// carries its priority/key_pool.
func TestResolve_ProviderModeMostSpecificWins(t *testing.T) {
	prio := 500
	active := map[ScopeLevel]Policy{
		LevelTenantProductCountry: {ProviderOverrides: map[string]Override{"prospeo": {Mode: "on", Priority: &prio, KeyPool: "prospeo:byo"}}},
		LevelTenantDefault:        {ProviderOverrides: map[string]Override{"prospeo": {Mode: "off"}}},
	}
	eff := Resolve(configver.ScopeDims{Product: "p", Country: "DE"}, active)
	got := eff.ProviderModes["prospeo"]
	if got.Mode != "on" || got.Source != LevelTenantProductCountry {
		t.Fatalf("mode=%q source=%v, want on @ level 1", got.Mode, got.Source)
	}
	if got.Priority == nil || *got.Priority != 500 || got.KeyPool != "prospeo:byo" {
		t.Fatalf("priority/key_pool did not travel with the winning entry: %+v", got)
	}
}

// TestResolve_ListsAreAtomic proves lists (order) are taken whole from the first definer, never
// merged element-wise.
func TestResolve_ListsAreAtomic(t *testing.T) {
	active := map[ScopeLevel]Policy{
		LevelTenantDefault:   {Waterfall: Waterfall{Order: []string{"a", "b"}}},
		LevelPlatformDefault: {Waterfall: Waterfall{Order: []string{"x", "y", "z"}}},
	}
	eff := Resolve(configver.ScopeDims{}, active)
	if eff.Order == nil || eff.Order.Source != LevelTenantDefault {
		t.Fatalf("order source = %+v, want tenant default", eff.Order)
	}
	if len(eff.Order.Value) != 2 || eff.Order.Value[0] != "a" {
		t.Fatalf("order should be the whole level-4 list [a b], got %v (no element merge)", eff.Order.Value)
	}
}
