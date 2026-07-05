package configver

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// --- in-memory fakes (no PG): exercise the Service lifecycle orchestration + epoch accounting ---

type memStore struct {
	mu       sync.Mutex
	versions map[string]*Version
	active   map[string]string // kind|scope -> version id
	epochs   map[string]int64  // kind -> epoch
	seq      int
}

func newMemStore() *memStore {
	return &memStore{versions: map[string]*Version{}, active: map[string]string{}, epochs: map[string]int64{}}
}

func key(kind, scope string) string { return kind + "|" + scope }

func (m *memStore) CreateDraft(_ context.Context, kind, scope string, payload json.RawMessage, parent, by string) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	next := 0
	for _, v := range m.versions {
		if v.Kind == kind && v.ScopeKey == scope && v.Version > next {
			next = v.Version
		}
	}
	id := newID()
	v := &Version{ID: id, TenantID: "acme", Kind: kind, ScopeKey: scope, Version: next + 1,
		Status: StatusDraft, Payload: payload, ParentVersionID: parent, CreatedBy: by}
	m.versions[id] = v
	return *v, nil
}

func (m *memStore) Get(_ context.Context, id string) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.versions[id]; ok {
		return *v, nil
	}
	return Version{}, ErrNotFound
}

func (m *memStore) GetByVersion(_ context.Context, kind, scope string, num int) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.versions {
		if v.Kind == kind && v.ScopeKey == scope && v.Version == num {
			return *v, nil
		}
	}
	return Version{}, ErrNotFound
}

func (m *memStore) PatchDraft(_ context.Context, id string, payload json.RawMessage) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[id]
	if !ok {
		return Version{}, ErrNotFound
	}
	if v.Status == StatusPublished || v.Status == StatusArchived {
		return Version{}, ErrVersionConflict
	}
	v.Payload = payload
	v.Status = StatusDraft
	v.PayloadHash = nil
	v.ValidationReport = nil
	return *v, nil
}

func (m *memStore) SaveValidation(_ context.Context, id string, report json.RawMessage, hash []byte, status string) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[id]
	if !ok || v.Status == StatusPublished || v.Status == StatusArchived {
		return Version{}, ErrVersionConflict
	}
	v.ValidationReport = report
	v.PayloadHash = hash
	v.Status = status
	return *v, nil
}

func (m *memStore) List(context.Context, string, string, db.Cursor, int) ([]Version, db.Cursor, error) {
	return nil, db.Cursor{}, nil
}
func (m *memStore) ListActive(context.Context, string, db.Cursor, int) ([]ActiveEntry, db.Cursor, error) {
	return nil, db.Cursor{}, nil
}
func (m *memStore) ListWorkflows(context.Context, db.Cursor, int) ([]WorkflowRow, db.Cursor, error) {
	return nil, db.Cursor{}, nil
}

func (m *memStore) ActiveVersionID(_ context.Context, kind, scope string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.active[key(kind, scope)]
	return id, ok, nil
}

func (m *memStore) ListEpochs(_ context.Context) ([]Epoch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Epoch{}
	for k, e := range m.epochs {
		out = append(out, Epoch{Kind: k, Epoch: e})
	}
	return out, nil
}

func (m *memStore) BumpEpoch(_ context.Context, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epochs[kind]++
	return nil
}

func (m *memStore) Publish(_ context.Context, p PublishParams, _ *workflowIndexRow, aud auditFn) (Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[p.VersionID]
	if !ok {
		return Version{}, ErrNotFound
	}
	// Status gate (mirrors PGStore): publish requires validated; rollback a prior published/archived.
	if p.Rollback {
		if v.Status != StatusArchived && v.Status != StatusPublished {
			return Version{}, ErrNotValidated
		}
	} else if v.Status != StatusValidated {
		return Version{}, ErrNotValidated
	}
	k := key(p.Kind, p.ScopeKey)
	prev, exists := m.active[k]
	if !exists {
		if p.ExpectedActiveVersionID != "" {
			return Version{}, ErrVersionConflict
		}
	} else {
		if prev != p.ExpectedActiveVersionID {
			return Version{}, ErrVersionConflict
		}
		if prev != p.VersionID {
			m.versions[prev].Status = StatusArchived
		}
	}
	m.active[k] = p.VersionID
	v.Status = StatusPublished
	m.epochs[p.Kind]++ // publish bumps in-tx
	if aud != nil {
		if err := aud(nil, prev); err != nil {
			return Version{}, err
		}
	}
	return *v, nil
}

var _ Store = (*memStore)(nil)

// fakeAudit records appends; AppendConn ignores the (nil) conn the memStore passes.
type fakeAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (f *fakeAudit) Append(_ context.Context, e audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return nil
}
func (f *fakeAudit) AppendConn(_ context.Context, _ *pg.Conn, e audit.Entry) error {
	return f.Append(context.Background(), e)
}

var _ Auditor = (*fakeAudit)(nil)

// passValidator returns an empty (clean) report.
type passValidator struct{}

func (passValidator) Validate(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"errors":[],"warnings":[]}`), nil
}

// failValidator returns one error-severity finding.
type failValidator struct{}

func (failValidator) Validate(context.Context, string, string, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"errors":[{"rule":"VR-1","code":"provider_unknown","severity":"error","path":"/","message":"x"}],"warnings":[]}`), nil
}

func testCtx() context.Context {
	return tenant.WithPrincipal(context.Background(), tenant.Principal{
		TenantID: "acme", UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:tenant_admin"},
	})
}

// TestServiceLifecycle exercises the full draft -> validate (report + hash) -> edit (reverts to
// draft, hash cleared) -> re-validate -> publish -> rollback path plus per-publish epoch accounting.
func TestServiceLifecycle(t *testing.T) {
	store := newMemStore()
	aud := &fakeAudit{}
	var bumps []string
	svc := New(Config{
		Store: store, Audit: aud,
		Validators: map[string]Validator{KindRoutingPolicy: passValidator{}},
		OnBump:     func(_, kind, _ string) { bumps = append(bumps, kind) },
	})
	ctx := testCtx()
	payload := json.RawMessage(`{"schema_version":1,"a":1}`)

	// draft
	v, err := svc.CreateDraft(ctx, KindRoutingPolicy, "default", payload)
	if err != nil || v.Status != StatusDraft || v.Version != 1 {
		t.Fatalf("create draft: %+v err=%v", v, err)
	}

	// validate -> validated, report stored, hash pinned
	v, err = svc.Validate(ctx, v.ID)
	if err != nil || v.Status != StatusValidated {
		t.Fatalf("validate: status=%q err=%v", v.Status, err)
	}
	if len(v.PayloadHash) == 0 || len(v.ValidationReport) == 0 {
		t.Fatalf("validate must pin hash + store report: hash=%d report=%d", len(v.PayloadHash), len(v.ValidationReport))
	}

	// edit -> reverts to draft, hash cleared
	v, err = svc.PatchDraft(ctx, v.ID, json.RawMessage(`{"schema_version":1,"a":2}`))
	if err != nil || v.Status != StatusDraft {
		t.Fatalf("patch: status=%q err=%v", v.Status, err)
	}
	if len(v.PayloadHash) != 0 {
		t.Fatal("patch after validate must clear the pinned hash")
	}

	// re-validate -> validated
	v, err = svc.Validate(ctx, v.ID)
	if err != nil || v.Status != StatusValidated {
		t.Fatalf("re-validate: %+v err=%v", v.Status, err)
	}

	// publish -> published, active points at it, epoch bumped once
	v1 := v
	pub, err := svc.Publish(ctx, v1.ID, nil)
	if err != nil || pub.Status != StatusPublished {
		t.Fatalf("publish: %+v err=%v", pub.Status, err)
	}
	if id, _, _ := store.ActiveVersionID(ctx, KindRoutingPolicy, "default"); id != v1.ID {
		t.Fatalf("config_active should point at v1, got %q", id)
	}
	if store.epochs[KindRoutingPolicy] != 1 {
		t.Fatalf("epoch after 1 publish = %d, want 1", store.epochs[KindRoutingPolicy])
	}

	// a second version, published -> v1 archived, epoch bumped again
	v2, _ := svc.CreateDraft(ctx, KindRoutingPolicy, "default", json.RawMessage(`{"schema_version":1,"a":3}`))
	if v2.ParentVersionID != v1.ID {
		t.Fatalf("new draft parent should be the active v1 (%s), got %q", v1.ID, v2.ParentVersionID)
	}
	v2, _ = svc.Validate(ctx, v2.ID)
	if _, err := svc.Publish(ctx, v2.ID, nil); err != nil {
		t.Fatalf("publish v2: %v", err)
	}
	if got, _ := store.Get(ctx, v1.ID); got.Status != StatusArchived {
		t.Fatalf("v1 should be archived after v2 publish, got %q", got.Status)
	}
	if store.epochs[KindRoutingPolicy] != 2 {
		t.Fatalf("epoch after 2 publishes = %d, want 2", store.epochs[KindRoutingPolicy])
	}

	// rollback to v1 (a publish of the prior version; nothing destroyed)
	rb, err := svc.Rollback(ctx, KindRoutingPolicy, "default", 1, ptr(v2.ID))
	if err != nil {
		t.Fatalf("rollback to v1: %v", err)
	}
	if rb.Status != StatusPublished {
		t.Fatalf("rolled-back v1 status = %q, want published", rb.Status)
	}
	if id, _, _ := store.ActiveVersionID(ctx, KindRoutingPolicy, "default"); id != v1.ID {
		t.Fatalf("after rollback config_active should point at v1, got %q", id)
	}
	if store.epochs[KindRoutingPolicy] != 3 {
		t.Fatalf("epoch after rollback = %d, want 3", store.epochs[KindRoutingPolicy])
	}
	if len(bumps) != 3 {
		t.Fatalf("OnBump fired %d times, want 3 (one per publish/rollback)", len(bumps))
	}
}

// TestServiceValidateWithErrorsStaysDraft proves a report with errors keeps the version in draft and
// leaves the hash unpinned.
func TestServiceValidateWithErrorsStaysDraft(t *testing.T) {
	store := newMemStore()
	svc := New(Config{Store: store, Audit: &fakeAudit{},
		Validators: map[string]Validator{KindRoutingPolicy: failValidator{}}})
	ctx := testCtx()
	v, _ := svc.CreateDraft(ctx, KindRoutingPolicy, "default", json.RawMessage(`{"x":1}`))
	v, err := svc.Validate(ctx, v.ID)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if v.Status != StatusDraft {
		t.Fatalf("a report with errors must keep status draft, got %q", v.Status)
	}
	if len(v.PayloadHash) != 0 {
		t.Fatal("a failing validation must not pin a hash")
	}
	// publishing a non-validated version is refused.
	if _, err := svc.Publish(ctx, v.ID, nil); err == nil {
		t.Fatal("publish of a non-validated version must fail")
	}
}

func ptr(s string) *string { return &s }
