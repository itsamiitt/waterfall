package store

import (
	"context"
	"sync"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Memory is an in-memory, concurrency-safe Store used for unit tests and local runs.
// It enforces tenant isolation (G1) the same way the Postgres backend does at the app
// layer: every key is namespaced by the tenant read from the context principal, so a
// read under tenant B can never observe tenant A's writes. The Postgres backend adds a
// second, datastore-level guarantee (FORCE RLS); this one proves the application
// contract deterministically without a database.
type Memory struct {
	mu sync.Mutex

	// idempotency[tenant][key] = result
	idempotency map[string]map[string]provider.Result
	// committed[tenant][jobID] = credits
	committed map[string]map[string]domain.Credits
	// versions[tenant][subjectID][field] = all observations (append order)
	versions map[string]map[string]map[domain.Field][]domain.FieldValue
}

// NewMemory builds an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		idempotency: map[string]map[string]provider.Result{},
		committed:   map[string]map[string]domain.Credits{},
		versions:    map[string]map[string]map[domain.Field][]domain.FieldValue{},
	}
}

var _ Store = (*Memory)(nil)

func (m *Memory) Lookup(ctx context.Context, key string) (provider.Result, bool, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return provider.Result{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res, ok := m.idempotency[t][key]
	return res, ok, nil
}

func (m *Memory) Record(ctx context.Context, key string, res provider.Result) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idempotency[t] == nil {
		m.idempotency[t] = map[string]provider.Result{}
	}
	if _, exists := m.idempotency[t][key]; exists {
		return nil // first writer wins
	}
	m.idempotency[t][key] = res
	return nil
}

func (m *Memory) Reserve(ctx context.Context, jobID string, amount, ceiling domain.Credits) (domain.Credits, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.committed[t] == nil {
		m.committed[t] = map[string]domain.Credits{}
	}
	cur := m.committed[t][jobID]
	if cur+amount > ceiling {
		return cur, ErrCeilingExceeded
	}
	cur += amount
	m.committed[t][jobID] = cur
	return cur, nil
}

func (m *Memory) Release(ctx context.Context, jobID string, amount domain.Credits) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.committed[t] == nil {
		return nil
	}
	cur := m.committed[t][jobID] - amount
	if cur < 0 {
		cur = 0
	}
	m.committed[t][jobID] = cur
	return nil
}

func (m *Memory) Committed(ctx context.Context, jobID string) (domain.Credits, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.committed[t][jobID], nil
}

func (m *Memory) Append(ctx context.Context, subjectID string, v domain.FieldValue) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	if !v.Valid() { // G5: reject bare/provenance-less values
		return domain.NewProviderError(v.Prov.Provider, domain.ClassBadRequest,
			errInvalidFieldValue)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.versions[t] == nil {
		m.versions[t] = map[string]map[domain.Field][]domain.FieldValue{}
	}
	if m.versions[t][subjectID] == nil {
		m.versions[t][subjectID] = map[domain.Field][]domain.FieldValue{}
	}
	m.versions[t][subjectID][v.Field] = append(m.versions[t][subjectID][v.Field], v)
	return nil
}

func (m *Memory) Current(ctx context.Context, subjectID string) (map[domain.Field]domain.FieldValue, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[domain.Field]domain.FieldValue{}
	for f, obs := range m.versions[t][subjectID] {
		best := obs[0]
		for _, o := range obs[1:] {
			if o.Confidence > best.Confidence {
				best = o
			}
		}
		out[f] = best
	}
	return out, nil
}

func (m *Memory) History(ctx context.Context, subjectID string, f domain.Field) ([]domain.FieldValue, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.versions[t][subjectID][f]
	out := make([]domain.FieldValue, len(src))
	copy(out, src)
	return out, nil
}
