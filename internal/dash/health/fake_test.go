package health

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/tenant"
)

// fakeStore is an in-memory Store for unit tests (no Postgres). Thread-safe so the scheduler's
// concurrent probes can call WriteCheck under the race detector.
type fakeStore struct {
	mu sync.Mutex

	writes  []writeRec
	targets []Target

	dayRows         map[string]DayRow
	hourRows        map[int64]HourAgg
	sample          WindowSample
	statuses        []ProviderStatus
	regions         []RegionAgg
	schedules       map[string]Schedule
	providerTargets map[string]Target
	exhausted       []string
	foldReturn      int
	foldErr         error
}

type writeRec struct {
	providerID string
	r          CheckResult
	at         time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		dayRows:         map[string]DayRow{},
		hourRows:        map[int64]HourAgg{},
		schedules:       map[string]Schedule{},
		providerTargets: map[string]Target{},
	}
}

func (f *fakeStore) WriteCheck(_ context.Context, providerID string, r CheckResult, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, writeRec{providerID, r, at})
	return nil
}

func (f *fakeStore) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *fakeStore) FoldDay(context.Context, time.Time) (int, error) {
	return f.foldReturn, f.foldErr
}

func (f *fakeStore) DayBuckets(context.Context, string, time.Time, time.Time) (map[string]DayRow, error) {
	return f.dayRows, nil
}

func (f *fakeStore) HourBuckets(context.Context, string, time.Time, time.Time) (map[int64]HourAgg, error) {
	return f.hourRows, nil
}

func (f *fakeStore) SampleWindow(context.Context, string, time.Time, time.Time) (WindowSample, error) {
	return f.sample, nil
}

func (f *fakeStore) ProviderStatuses(context.Context) ([]ProviderStatus, error) {
	return f.statuses, nil
}

func (f *fakeStore) Regional(context.Context, time.Time, time.Time) ([]RegionAgg, error) {
	return f.regions, nil
}

func (f *fakeStore) ListCheckTargets(context.Context) ([]Target, error) {
	return f.targets, nil
}

func (f *fakeStore) ProviderTarget(_ context.Context, providerID string) (Target, bool, error) {
	t, ok := f.providerTargets[providerID]
	return t, ok, nil
}

func (f *fakeStore) ListSchedules(context.Context) ([]Schedule, error) {
	out := make([]Schedule, 0, len(f.schedules))
	for _, s := range f.schedules {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) UpsertSchedule(_ context.Context, s Schedule) (Schedule, error) {
	s.UpdatedAt = time.Unix(0, 0).UTC()
	f.schedules[s.ProviderID] = s
	return s, nil
}

func (f *fakeStore) ExhaustedKeys(_ context.Context, limit int) ([]string, error) {
	if limit < len(f.exhausted) {
		return f.exhausted[:limit], nil
	}
	return f.exhausted, nil
}

// fakeAuth binds a fixed Principal.
type fakeAuth struct{ p tenant.Principal }

func (f fakeAuth) Authenticate(*http.Request) (tenant.Principal, error) { return f.p, nil }

func operatorPrincipal() tenant.Principal {
	return tenant.Principal{TenantID: "platform", UserID: "00000000-0000-4000-8000-000000000001", Scopes: []string{"role:operator"}}
}

func tenantPrincipal() tenant.Principal {
	return tenant.Principal{TenantID: "acme", UserID: "00000000-0000-4000-8000-000000000009", Scopes: []string{"role:tenant_user"}}
}

// fakeReactivator records the keys it was asked to probe; failKeys always error.
type fakeReactivator struct {
	mu       sync.Mutex
	probed   []string
	failKeys map[string]bool
}

func (r *fakeReactivator) Probe(_ context.Context, keyID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probed = append(r.probed, keyID)
	if r.failKeys[keyID] {
		return context.DeadlineExceeded
	}
	return nil
}
