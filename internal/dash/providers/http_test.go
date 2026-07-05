package providers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// --- test doubles ---

// fakeStore is an in-memory Store for offline handler tests. It applies colVal sets to Provider
// values, mirroring the pgstore semantics the handlers rely on.
type fakeStore struct {
	rows map[string]Provider
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]Provider{}} }

func (s *fakeStore) Insert(_ context.Context, cols []colVal) (Provider, error) {
	var p Provider
	applyColVals(&p, cols)
	if _, ok := s.rows[p.ID]; ok {
		return Provider{}, ErrConflict
	}
	now := time.Unix(1700000000, 0).UTC()
	p.CreatedAt, p.UpdatedAt = &now, &now
	s.rows[p.ID] = p
	return p, nil
}

func (s *fakeStore) Update(_ context.Context, id string, cols []colVal) (Provider, error) {
	p, ok := s.rows[id]
	if !ok {
		return Provider{}, ErrNotFound
	}
	applyColVals(&p, cols)
	now := time.Unix(1700000001, 0).UTC()
	p.UpdatedAt = &now
	s.rows[id] = p
	return p, nil
}

func (s *fakeStore) Delete(_ context.Context, id string) (bool, error) {
	if _, ok := s.rows[id]; !ok {
		return false, nil
	}
	delete(s.rows, id)
	return true, nil
}

func (s *fakeStore) GetFull(_ context.Context, id string) (Provider, error) {
	p, ok := s.rows[id]
	if !ok {
		return Provider{}, ErrNotFound
	}
	return p, nil
}

func (s *fakeStore) GetCatalog(ctx context.Context, id string) (Provider, error) {
	p, err := s.GetFull(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if p.Visibility != VisibilityTenantReadable {
		return Provider{}, ErrNotFound
	}
	return projectCatalog(p), nil
}

func (s *fakeStore) sorted() []Provider {
	out := make([]Provider, 0, len(s.rows))
	for _, p := range s.rows {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *fakeStore) ListFull(_ context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	return page(filterRows(s.sorted(), f, false), cur, limit), db.Cursor{}, nil
}

func (s *fakeStore) ListCatalog(_ context.Context, f Filter, cur db.Cursor, limit int) ([]Provider, db.Cursor, error) {
	var live []Provider
	for _, p := range s.sorted() {
		if p.Visibility == VisibilityTenantReadable {
			live = append(live, projectCatalog(p))
		}
	}
	return page(filterRows(live, f, true), cur, limit), db.Cursor{}, nil
}

func (s *fakeStore) GetManyFull(_ context.Context, ids []string) ([]Provider, error) {
	var out []Provider
	for _, id := range ids {
		if p, ok := s.rows[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func filterRows(in []Provider, f Filter, catalog bool) []Provider {
	var out []Provider
	for _, p := range in {
		if f.Status != "" && p.Status != f.Status {
			continue
		}
		if f.OpState != "" && !catalog && p.OpState != f.OpState {
			continue
		}
		if f.Category != "" && p.Category != f.Category {
			continue
		}
		if f.Q != "" && !strings.HasPrefix(strings.ToLower(p.DisplayName), strings.ToLower(f.Q)) &&
			!strings.HasPrefix(strings.ToLower(p.ID), strings.ToLower(f.Q)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func page(in []Provider, cur db.Cursor, limit int) []Provider {
	limit = db.ClampLimit(limit)
	start := 0
	if cur.ID != "" {
		for i, p := range in {
			if p.ID > cur.ID {
				start = i
				break
			}
			start = i + 1
		}
	}
	end := start + limit
	if end > len(in) {
		end = len(in)
	}
	return in[start:end]
}

// projectCatalog blanks the platform-only fields the providers_catalog view omits (op_state,
// scores, limits, credit balances), so the tenant path exercises the projection contract.
func projectCatalog(p Provider) Provider {
	return Provider{
		ID: p.ID, DisplayName: p.DisplayName, Category: p.Category, Description: p.Description,
		LogoURL: p.LogoURL, Status: p.Status, Capabilities: p.Capabilities, Region: p.Region,
		DocsURL: p.DocsURL, Tags: p.Tags, SunsetAt: p.SunsetAt, ArchivedAt: p.ArchivedAt,
		Visibility: VisibilityTenantReadable,
	}
}

func applyColVals(p *Provider, cols []colVal) {
	for _, c := range cols {
		switch c.name {
		case "id":
			p.ID = vstr(c.val)
		case "display_name":
			p.DisplayName = vstr(c.val)
		case "category":
			p.Category = vstr(c.val)
		case "description":
			p.Description = vstr(c.val)
		case "status":
			p.Status = vstr(c.val)
		case "compliance_review_status":
			p.ComplianceReviewStatus = vstr(c.val)
		case "op_state":
			p.OpState = vstr(c.val)
		case "visibility":
			p.Visibility = vstr(c.val)
		case "base_url":
			p.BaseURL = vstr(c.val)
		case "auth_scheme":
			p.AuthScheme = vstr(c.val)
		case "auth_header":
			p.AuthHeader = vstr(c.val)
		case "priority":
			p.Priority = vi64(c.val)
		case "timeout_ms":
			p.TimeoutMS = vi64(c.val)
		case "credits_remaining":
			p.CreditsRemaining = vi64(c.val)
		case "health_score":
			p.HealthScore = vf64(c.val)
		case "capabilities":
			_ = json.Unmarshal([]byte(vstr(c.val)), &p.Capabilities)
		case "region":
			p.Region = parseArrVal(c.val)
		case "tags":
			p.Tags = parseArrVal(c.val)
		case "archived_at":
			p.ArchivedAt = vts(c.val)
		case "last_sync_at":
			p.LastSyncAt = vts(c.val)
		case "last_health_at":
			p.LastHealthAt = vts(c.val)
		case "last_success_at":
			p.LastSuccessAt = vts(c.val)
		case "last_failure_at":
			p.LastFailureAt = vts(c.val)
		}
	}
}

func vstr(v any) string {
	s, _ := v.(string)
	return s
}
func vi64(v any) *int64 {
	if n, ok := v.(int64); ok {
		return &n
	}
	return nil
}
func vf64(v any) *float64 {
	if n, ok := v.(float64); ok {
		return &n
	}
	return nil
}
func vts(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		return &t
	}
	return nil
}
func parseArrVal(v any) []string {
	s := vstr(v)
	return parseTextArray(&s)
}

// fakeAuditor records appended entries.
type fakeAuditor struct{ entries []audit.Entry }

func (a *fakeAuditor) Append(_ context.Context, e audit.Entry) error {
	a.entries = append(a.entries, e)
	return nil
}

// fakeAuth binds a fixed principal.
type fakeAuth struct{ p tenant.Principal }

func (a fakeAuth) Authenticate(*http.Request) (tenant.Principal, error) { return a.p, nil }

func principal(role string) tenant.Principal {
	tid := "platform"
	if role != "operator" {
		tid = "acme"
	}
	return tenant.Principal{TenantID: tid, UserID: "u1", Scopes: []string{"role:" + role}}
}

// newTestServer wires a fake-backed handlers table for role.
func newTestServer(t *testing.T, role string) (*httptest.Server, *fakeStore, *fakeAuditor) {
	t.Helper()
	store := newFakeStore()
	aud := &fakeAuditor{}
	svc := newService(store, aud, nil, func() time.Time { return time.Unix(1700000000, 0).UTC() })
	h := &handlers{svc: svc, auth: fakeAuth{principal(role)}, idem: newIdemLedger(), log: slog.Default()}
	mux := http.NewServeMux()
	registerRoutes(mux, h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, store, aud
}

func do(t *testing.T, ts *httptest.Server, method, path, idemKey, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, r)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

const createBody = `{"id":"hunter","display_name":"Hunter","category":"email_finder",` +
	`"base_url":"https://api.hunter.io","auth_scheme":"api-key-header","auth_header":"X-API-Key",` +
	`"capabilities":[{"field":"work_email","cost_credits":1,"expected_confidence":0.9}]}`

// --- tests ---

func TestCreateGetPatchEnable_RoundTrip(t *testing.T) {
	ts, _, aud := newTestServer(t, "operator")

	st, body := do(t, ts, "POST", basePath, "k1", createBody)
	if st != 201 {
		t.Fatalf("create = %d %v, want 201", st, body)
	}
	if body["status"] != StatusDeprioritized || body["op_state"] != OpDisabled {
		t.Fatalf("create defaults wrong: %v", body)
	}
	if body["effective_available"] != false || body["unavailable_reason"] != ReasonStatusDeprioritized {
		t.Fatalf("new provider must be unavailable (status_deprioritized): %v", body)
	}

	if st, _ := do(t, ts, "GET", basePath+"/hunter", "", ""); st != 200 {
		t.Fatalf("get = %d, want 200", st)
	}

	// Approve + promote to ACTIVE-CANDIDATE; still disabled => op_state_disabled.
	st, body = do(t, ts, "PATCH", basePath+"/hunter", "k2",
		`{"status":"ACTIVE-CANDIDATE","compliance_review_status":"approved"}`)
	if st != 200 || body["unavailable_reason"] != ReasonOpStateDisabled {
		t.Fatalf("patch = %d %v, want 200 op_state_disabled", st, body)
	}

	// Enable => available.
	st, body = do(t, ts, "POST", basePath+"/hunter/enable", "k3", `{"reason":"go live"}`)
	if st != 200 || body["op_state"] != OpEnabled || body["effective_available"] != true {
		t.Fatalf("enable = %d %v, want 200 enabled+available", st, body)
	}

	// Re-enable (no-op transition) => 422.
	if st, m := do(t, ts, "POST", basePath+"/hunter/enable", "k4", ``); st != 422 {
		t.Fatalf("re-enable = %d %v, want 422 invalid transition", st, m)
	}

	// Each mutation appended an audit row.
	actions := map[string]bool{}
	for _, e := range aud.entries {
		actions[e.Action] = true
		if e.ObjectKind != "providers" {
			t.Errorf("audit object_kind = %q, want providers", e.ObjectKind)
		}
	}
	for _, want := range []string{"provider_create", "provider_update", "provider_enable"} {
		if !actions[want] {
			t.Errorf("missing audit action %q (got %v)", want, actions)
		}
	}
}

func TestCreate_Validation(t *testing.T) {
	ts, _, _ := newTestServer(t, "operator")
	cases := []struct {
		name, body, wantCode string
		wantStatus           int
	}{
		{"missing id", `{"display_name":"X"}`, codeValidationFailed, 422},
		{"missing display_name", `{"id":"x"}`, codeValidationFailed, 422},
		{"bad status", `{"id":"x","display_name":"X","status":"NOPE"}`, codeValidationFailed, 422},
		{"unknown field", `{"id":"x","display_name":"X","wat":1}`, codeInvalidJSON, 400},
	}
	for _, c := range cases {
		st, body := do(t, ts, "POST", basePath, "idem-"+c.name, c.body)
		if st != c.wantStatus || errCode(body) != c.wantCode {
			t.Errorf("%s: got %d %s, want %d %s", c.name, st, errCode(body), c.wantStatus, c.wantCode)
		}
	}
}

func TestGet_NotFound(t *testing.T) {
	ts, _, _ := newTestServer(t, "operator")
	if st, m := do(t, ts, "GET", basePath+"/ghost", "", ""); st != 404 || errCode(m) != codeNotFound {
		t.Fatalf("missing get = %d %s, want 404 not_found", st, errCode(m))
	}
}

func TestRBAC_TenantCannotWrite(t *testing.T) {
	ts, _, _ := newTestServer(t, "tenant_admin")
	st, body := do(t, ts, "POST", basePath, "k1", createBody)
	if st != 403 || errCode(body) != codeForbidden {
		t.Fatalf("tenant create = %d %s, want 403 forbidden", st, errCode(body))
	}
}

func TestRBAC_TenantReadGetsProjection(t *testing.T) {
	// Seed as operator, then read as tenant_user through a second server sharing the store.
	opTS, store, _ := newTestServer(t, "operator")
	if st, _ := do(t, opTS, "POST", basePath, "seed", createBody); st != 201 {
		t.Fatal("seed create failed")
	}
	// Promote so it is visibly tenant_readable + active (created default is tenant_readable).
	if st, _ := do(t, opTS, "PATCH", basePath+"/hunter", "seed2",
		`{"status":"ACTIVE-CANDIDATE","compliance_review_status":"approved"}`); st != 200 {
		t.Fatal("seed patch failed")
	}

	tenantSvc := newService(store, &fakeAuditor{}, nil, time.Now)
	th := &handlers{svc: tenantSvc, auth: fakeAuth{principal("tenant_user")}, idem: newIdemLedger(), log: slog.Default()}
	tmux := http.NewServeMux()
	registerRoutes(tmux, th)
	tts := httptest.NewServer(tmux)
	defer tts.Close()

	st, body := do(t, tts, "GET", basePath+"/hunter", "", "")
	if st != 200 {
		t.Fatalf("tenant get = %d, want 200", st)
	}
	// Projection: op_state, base_url, and scores must be absent; catalog fields present.
	if _, ok := body["op_state"]; ok {
		t.Errorf("tenant projection leaked op_state: %v", body)
	}
	if _, ok := body["base_url"]; ok {
		t.Errorf("tenant projection leaked base_url: %v", body)
	}
	if body["capabilities"] == nil {
		t.Errorf("tenant projection dropped capabilities: %v", body)
	}
	// effective_available is still present (status-conjunct only for the tenant view).
	if _, ok := body["effective_available"]; !ok {
		t.Errorf("tenant projection must still carry effective_available: %v", body)
	}

	// A tenant hitting an operator-only route (rankings) is 403.
	if st, m := do(t, tts, "GET", basePath+"/rankings", "", ""); st != 403 || errCode(m) != codeForbidden {
		t.Errorf("tenant rankings = %d %s, want 403 forbidden", st, errCode(m))
	}
}

func TestIdempotency_ReplayAndConflict(t *testing.T) {
	ts, _, _ := newTestServer(t, "operator")

	// Missing key on a write => 400.
	if st, m := do(t, ts, "POST", basePath, "", createBody); st != 400 || errCode(m) != codeMissingIdemKey {
		t.Fatalf("missing idem key = %d %s, want 400 missing_idempotency_key", st, errCode(m))
	}
	// First create.
	if st, _ := do(t, ts, "POST", basePath, "same", createBody); st != 201 {
		t.Fatal("first create not 201")
	}
	// Same key + same body => replayed 201 (not a 409 conflict from the store).
	if st, _ := do(t, ts, "POST", basePath, "same", createBody); st != 201 {
		t.Fatal("idempotent replay not 201")
	}
	// Same key + different body => 409 idempotency_key_reuse.
	if st, m := do(t, ts, "POST", basePath, "same", `{"id":"other","display_name":"Other"}`); st != 409 ||
		errCode(m) != codeIdempotencyReuse {
		t.Fatalf("idem reuse = %d %s, want 409 idempotency_key_reuse", st, errCode(m))
	}
}

func TestDeleteAndArchive_Audited(t *testing.T) {
	ts, store, aud := newTestServer(t, "operator")
	if st, _ := do(t, ts, "POST", basePath, "k1", createBody); st != 201 {
		t.Fatal("create failed")
	}
	// Archive => soft delete stamps archived_at.
	st, body := do(t, ts, "POST", basePath+"/hunter/archive", "k2", ``)
	if st != 200 || body["archived_at"] == nil {
		t.Fatalf("archive = %d %v, want 200 with archived_at", st, body)
	}
	// Delete => hard delete.
	if st, _ := do(t, ts, "DELETE", basePath+"/hunter", "k3", ``); st != 200 {
		t.Fatal("delete not 200")
	}
	if _, ok := store.rows["hunter"]; ok {
		t.Fatal("row not hard-deleted")
	}
	// Both actions audited.
	got := map[string]bool{}
	for _, e := range aud.entries {
		got[e.Action] = true
	}
	if !got["provider_archive"] || !got["provider_delete"] {
		t.Errorf("missing archive/delete audit rows: %v", got)
	}
}

func TestListPagination_Bounded(t *testing.T) {
	ts, _, _ := newTestServer(t, "operator")
	for _, id := range []string{"a", "b", "c"} {
		body := `{"id":"` + id + `","display_name":"` + strings.ToUpper(id) + `"}`
		if st, _ := do(t, ts, "POST", basePath, "seed-"+id, body); st != 201 {
			t.Fatalf("seed %s failed", id)
		}
	}
	st, body := do(t, ts, "GET", basePath+"?limit=2", "", "")
	if st != 200 {
		t.Fatalf("list = %d", st)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("limit=2 returned %d items", len(items))
	}
	// Over-cap limit => 400 invalid_filter.
	if st, m := do(t, ts, "GET", basePath+"?limit=500", "", ""); st != 400 || errCode(m) != codeInvalidFilter {
		t.Errorf("limit=500 = %d %s, want 400 invalid_filter", st, errCode(m))
	}
}
