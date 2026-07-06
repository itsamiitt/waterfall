package overview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/alerts"
	"github.com/enrichment/waterfall/internal/tenant"
)

// TestTileVocabulary pins the NORMATIVE 19-tile set (doc 09 §1.2, OI-WF-2): TileIDs, the
// drill map, and the Tiles JSON shape must agree exactly — no orphan tiles in either
// direction.
func TestTileVocabulary(t *testing.T) {
	if len(TileIDs) != 19 {
		t.Fatalf("tile vocabulary = %d entries, want 19 (doc 09 §1.2)", len(TileIDs))
	}
	if len(Drills) != len(TileIDs) {
		t.Fatalf("drill map = %d entries, want %d", len(Drills), len(TileIDs))
	}
	for _, id := range TileIDs {
		if !ValidTile(id) {
			t.Errorf("tile %q missing from drill map", id)
		}
	}
	// The marshaled Tiles struct must expose exactly the 19 tile keys.
	b, err := json.Marshal(Tiles{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != len(TileIDs) {
		t.Fatalf("Tiles JSON keys = %d, want %d", len(m), len(TileIDs))
	}
	for _, id := range TileIDs {
		if _, ok := m[id]; !ok {
			t.Errorf("Tiles JSON missing tile %q", id)
		}
	}
	// system_health is the single non-navigating tile.
	if Drills["system_health"].Route != "" {
		t.Error("system_health must have no drill route (doc 09 §1.3)")
	}
	if Drills["dlq_depth"].Route != "/dead-letters" {
		t.Errorf("dlq_depth drill = %+v", Drills["dlq_depth"])
	}
}

// TestMetaEnumsParity (meta/enums parity spot-check): the closed vocabularies served at
// GET /v1/admin/meta/enums come from the owning packages' constants — 17 alert metrics incl.
// cost.anomaly (OI-P6-1), 8 SSE topics, 9 event names, 9 key statuses, 12 strategies.
func TestMetaEnumsParity(t *testing.T) {
	h := &handlers{}
	req := httptest.NewRequest("GET", "/v1/admin/meta/enums", nil)
	req = req.WithContext(tenant.WithPrincipal(req.Context(),
		tenant.Principal{TenantID: "t1", UserID: "u1", Scopes: []string{"role:tenant_user"}}))
	rec := httptest.NewRecorder()
	h.enums(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	var metrics []map[string]any
	if err := json.Unmarshal(body["alert_metrics"], &metrics); err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 17 || len(metrics) != len(alerts.Metrics) {
		t.Fatalf("alert_metrics = %d entries, want 17 (doc 10 §4 + cost.anomaly)", len(metrics))
	}
	names := map[string]bool{}
	for _, m := range metrics {
		names[m["metric"].(string)] = true
	}
	for _, want := range []string{"cost.anomaly", "system.sse_clients", "system.aggregator_lag_s"} {
		if !names[want] {
			t.Errorf("alert_metrics missing %q", want)
		}
	}

	count := func(key string) int {
		var ss []any
		if err := json.Unmarshal(body[key], &ss); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return len(ss)
	}
	for key, want := range map[string]int{
		"sse_topics":            8,
		"sse_event_names":       9,
		"key_statuses":          9,
		"pool_strategies":       12,
		"provider_op_states":    4,
		"config_kinds":          5,
		"config_statuses":       4,
		"worker_states":         6,
		"approval_action_kinds": 6,
		"approval_states":       7,
		"alert_channel_types":   5,
	} {
		if got := count(key); got != want {
			t.Errorf("%s = %d entries, want %d", key, got, want)
		}
	}
}

// TestOverviewRBAC: no principal -> 401; unknown-tile 404 is a handler concern (needs a
// snapshot, covered in the integration test) — here we pin the fail-closed auth edge.
func TestOverviewRBAC(t *testing.T) {
	h := &handlers{auth: nil}
	req := httptest.NewRequest("GET", "/v1/admin/overview", nil)
	rec := httptest.NewRecorder()
	h.read(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") })(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
