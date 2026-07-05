package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/tenant"
)

// newTestServer wires a fake-backed handlers behind registerRoutes with the given Principal.
func newTestServer(t *testing.T, fake *fakeStore, p tenant.Principal, check CheckFunc) *httptest.Server {
	t.Helper()
	fixed := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	d := Deps{store: fake, Auth: fakeAuth{p: p}, Check: check, Now: func() time.Time { return fixed }}
	svc := NewService(d)
	h := &handlers{svc: svc, auth: d.Auth, idem: newIdemLedger(), log: slog.Default()}
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, idem, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}

// TestHTTP_TimelineNoDataNotUp exercises GET /health/providers/{id}/timeline over a 3-day window
// where one day has no folded row: the endpoint returns 3 contiguous buckets and the empty day is
// no_data, never up (acceptance #4 through the HTTP surface).
func TestHTTP_TimelineNoDataNotUp(t *testing.T) {
	fake := newFakeStore()
	fake.dayRows = map[string]DayRow{
		"2026-04-01": {Checks: 10, OK: 10, LatSumMS: 300},
		"2026-04-03": {Checks: 6, OK: 1, Down: 5, WorstErrorClass: "PROVIDER_DOWN"},
		// 2026-04-02 deliberately absent -> no_data
	}
	fake.sample = WindowSample{Lats: []int{10, 20, 30}, Checks: 16, OK: 11}

	ts := newTestServer(t, fake, operatorPrincipal(), nil)
	st, body := do(t, "GET", ts.URL+basePath+"/providers/hunter/timeline?granularity=day&from=2026-04-01T00:00:00Z&to=2026-04-03T12:00:00Z", "", "")
	if st != 200 {
		t.Fatalf("timeline = %d, want 200 (%v)", st, body)
	}
	buckets, _ := body["buckets"].([]any)
	if len(buckets) != 3 {
		t.Fatalf("want 3 day-buckets, got %d", len(buckets))
	}
	wantStatus := []string{StatusUp, StatusNoData, StatusDown}
	for i, b := range buckets {
		bm := b.(map[string]any)
		if bm["status"] != wantStatus[i] {
			t.Errorf("bucket[%d] status=%v want %s", i, bm["status"], wantStatus[i])
		}
		if bm["checks"].(float64) == 0 && bm["status"] == StatusUp {
			t.Fatalf("bucket[%d] zero checks rendered up", i)
		}
	}
	summary, _ := body["summary"].(map[string]any)
	if summary["uptime_pct"].(float64) <= 0 {
		t.Errorf("summary uptime missing: %v", summary)
	}
}

func TestHTTP_RBAC(t *testing.T) {
	fake := newFakeStore()

	// tenant role => 403 on an operator-only read.
	ts := newTestServer(t, fake, tenantPrincipal(), nil)
	if st, _ := do(t, "GET", ts.URL+basePath+"/providers", "", ""); st != http.StatusForbidden {
		t.Fatalf("tenant GET /providers = %d, want 403", st)
	}

	// operator role => 200.
	ts2 := newTestServer(t, fake, operatorPrincipal(), nil)
	if st, _ := do(t, "GET", ts2.URL+basePath+"/providers", "", ""); st != 200 {
		t.Fatalf("operator GET /providers = %d, want 200", st)
	}
}

func TestHTTP_ScheduleCRUD(t *testing.T) {
	fake := newFakeStore()
	ts := newTestServer(t, fake, operatorPrincipal(), nil)

	// PUT requires an Idempotency-Key.
	if st, _ := do(t, "PUT", ts.URL+basePath+"/schedules/hunter", "", `{"interval_s":30}`); st != http.StatusBadRequest {
		t.Fatalf("PUT without idem key = %d, want 400", st)
	}

	// Valid PUT persists.
	st, body := do(t, "PUT", ts.URL+basePath+"/schedules/hunter", "s1", `{"interval_s":30,"jitter_pct":15,"enabled":true}`)
	if st != 200 || body["interval_s"].(float64) != 30 {
		t.Fatalf("PUT schedule = %d %v", st, body)
	}

	// Invalid interval => 422.
	if st, _ := do(t, "PUT", ts.URL+basePath+"/schedules/hunter", "s2", `{"interval_s":1}`); st != http.StatusUnprocessableEntity {
		t.Fatalf("PUT invalid interval = %d, want 422", st)
	}

	// GET reflects the persisted row.
	st, body = do(t, "GET", ts.URL+basePath+"/schedules", "", "")
	items, _ := body["items"].([]any)
	if st != 200 || len(items) != 1 {
		t.Fatalf("GET schedules = %d %v", st, body)
	}
}

func TestHTTP_ChecksRun(t *testing.T) {
	fake := newFakeStore()
	fake.providerTargets["hunter"] = Target{ProviderID: "hunter", BaseURL: "http://x"}
	check := func(_ context.Context, tg Target) CheckResult {
		return CheckResult{Status: StatusUp, LatencyMS: 12}
	}
	ts := newTestServer(t, fake, operatorPrincipal(), check)

	st, body := do(t, "POST", ts.URL+basePath+"/checks/run", "c1", `{"provider_id":"hunter"}`)
	if st != 200 {
		t.Fatalf("checks/run = %d %v", st, body)
	}
	res, _ := body["result"].(map[string]any)
	if res["Status"] != StatusUp && res["status"] != StatusUp {
		t.Fatalf("checks/run result status missing: %v", body)
	}
	if fake.writeCount() != 1 {
		t.Fatalf("checks/run wrote %d rows, want 1", fake.writeCount())
	}

	// Unknown provider => 404.
	if st, _ := do(t, "POST", ts.URL+basePath+"/checks/run", "c2", `{"provider_id":"ghost"}`); st != http.StatusNotFound {
		t.Fatalf("checks/run unknown = %d, want 404", st)
	}
}

func TestHTTP_Regional(t *testing.T) {
	fake := newFakeStore()
	fake.regions = []RegionAgg{{Region: "us", Checks: 10, OK: 9, UptimePct: 90}}
	ts := newTestServer(t, fake, operatorPrincipal(), nil)

	st, body := do(t, "GET", ts.URL+basePath+"/regional", "", "")
	items, _ := body["items"].([]any)
	if st != 200 || len(items) != 1 {
		t.Fatalf("regional = %d %v", st, body)
	}
}
