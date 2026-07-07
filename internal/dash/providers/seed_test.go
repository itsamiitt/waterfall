package providers

import (
	"context"
	"testing"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// fakeSeedStore is an in-memory Store implementation exercising only Insert/Update — enough to
// prove Seed's UPSERT + "don't clobber operator lifecycle state" behavior without a live DB.
type fakeSeedStore struct{ rows map[string]map[string]any }

func newFakeSeedStore() *fakeSeedStore { return &fakeSeedStore{rows: map[string]map[string]any{}} }

func colMap(cols []colVal) map[string]any {
	m := make(map[string]any, len(cols))
	for _, c := range cols {
		m[c.name] = c.val
	}
	return m
}

func providerFromMap(id string, m map[string]any) Provider {
	p := Provider{ID: id}
	if v, ok := m["status"].(string); ok {
		p.Status = v
	}
	if v, ok := m["display_name"].(string); ok {
		p.DisplayName = v
	}
	if v, ok := m["base_url"].(string); ok {
		p.BaseURL = v
	}
	if v, ok := m["category"].(string); ok {
		p.Category = v
	}
	return p
}

func (f *fakeSeedStore) Insert(_ context.Context, cols []colVal) (Provider, error) {
	m := colMap(cols)
	id, _ := m["id"].(string)
	if _, ok := f.rows[id]; ok {
		return Provider{}, ErrConflict
	}
	f.rows[id] = m
	return providerFromMap(id, m), nil
}

func (f *fakeSeedStore) Update(_ context.Context, id string, cols []colVal) (Provider, error) {
	existing, ok := f.rows[id]
	if !ok {
		return Provider{}, ErrNotFound
	}
	for k, v := range colMap(cols) {
		existing[k] = v
	}
	return providerFromMap(id, existing), nil
}

func (f *fakeSeedStore) Delete(context.Context, string) (bool, error) { return false, nil }
func (f *fakeSeedStore) GetFull(context.Context, string) (Provider, error) {
	return Provider{}, ErrNotFound
}
func (f *fakeSeedStore) ListFull(context.Context, Filter, db.Cursor, int) ([]Provider, db.Cursor, error) {
	return nil, db.Cursor{}, nil
}
func (f *fakeSeedStore) GetManyFull(context.Context, []string) ([]Provider, error) { return nil, nil }
func (f *fakeSeedStore) GetCatalog(context.Context, string) (Provider, error) {
	return Provider{}, ErrNotFound
}
func (f *fakeSeedStore) ListCatalog(context.Context, Filter, db.Cursor, int) ([]Provider, db.Cursor, error) {
	return nil, db.Cursor{}, nil
}

func TestSeed_CreateThenIdempotentRefresh(t *testing.T) {
	st := newFakeSeedStore()
	ctx := context.Background()
	in := SeedInput{
		ID: "hunter", DisplayName: "Hunter", Category: "email-find",
		Status: StatusActiveCandidate, BaseURL: "https://api.hunter.io/v2/email-finder",
		AuthScheme: "api-key-query", AuthQueryParam: "api_key",
		Capabilities: []Capability{{Field: "work_email", CostCredits: 10, ExpectedConfidence: 0.85}},
		Region:       []string{"global"},
	}

	// First seed: created.
	p, created, err := Seed(ctx, st, in)
	if err != nil || !created {
		t.Fatalf("first Seed: created=%v err=%v", created, err)
	}
	if p.Status != StatusActiveCandidate || p.BaseURL != in.BaseURL {
		t.Fatalf("seeded row wrong: %+v", p)
	}

	// Simulate an operator promoting the row's status out-of-band.
	st.rows["hunter"]["status"] = StatusDeprioritized

	// Re-seed with a NEW status + updated base URL: refresh only the descriptor.
	in.Status = StatusActiveCandidate
	in.BaseURL = "https://api.hunter.io/v2/email-finder-v3"
	p, created, err = Seed(ctx, st, in)
	if err != nil || created {
		t.Fatalf("re-Seed: created=%v err=%v (want refresh)", created, err)
	}
	if p.Status != StatusDeprioritized {
		t.Errorf("re-seed must NOT clobber operator status: got %q", p.Status)
	}
	if p.BaseURL != in.BaseURL {
		t.Errorf("re-seed must refresh descriptor base_url: got %q", p.BaseURL)
	}
}

func TestSeed_Rejects(t *testing.T) {
	st := newFakeSeedStore()
	if _, _, err := Seed(context.Background(), st, SeedInput{Status: StatusActiveCandidate}); err == nil {
		t.Error("empty id must error")
	}
	if _, _, err := Seed(context.Background(), st, SeedInput{ID: "x", Status: "BOGUS"}); err != ErrValidation {
		t.Errorf("invalid status must be ErrValidation, got %v", err)
	}
}
