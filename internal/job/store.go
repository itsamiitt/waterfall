package job

import (
	"context"
	"sync"

	"github.com/enrichment/waterfall/internal/tenant"
)

// Store persists jobs, tenant-scoped by the context principal (G1). A job created under
// tenant A is invisible to tenant B — a Get under B returns (nil, false), which the API
// surfaces as 404 (no cross-tenant existence disclosure).
type Store interface {
	// Put inserts or updates a job within the caller's tenant.
	Put(ctx context.Context, j *Job) error
	// Get returns a job by id within the caller's tenant.
	Get(ctx context.Context, id string) (*Job, bool, error)
}

// MemoryStore is an in-memory, concurrency-safe Store.
type MemoryStore struct {
	mu   sync.Mutex
	jobs map[string]map[string]*Job // tenant -> id -> job
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: map[string]map[string]*Job{}}
}

var _ Store = (*MemoryStore)(nil)

func (m *MemoryStore) Put(ctx context.Context, j *Job) error {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.jobs[t] == nil {
		m.jobs[t] = map[string]*Job{}
	}
	cp := *j // store a copy so concurrent worker mutation doesn't race a reader
	m.jobs[t][j.ID] = &cp
	return nil
}

func (m *MemoryStore) Get(ctx context.Context, id string) (*Job, bool, error) {
	t, err := tenant.TenantID(ctx)
	if err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[t][id]
	if !ok {
		return nil, false, nil
	}
	cp := *j
	return &cp, true, nil
}
