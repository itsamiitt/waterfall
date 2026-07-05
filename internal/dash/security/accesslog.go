package security

import (
	"context"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// AccessRecord is one row destined for api_access_log (doc 05 §11). route is the mux template,
// never the concrete path or query string; no bodies, headers, or PII beyond ip and user id.
type AccessRecord struct {
	TenantID string
	UserID   string
	Method   string
	Route    string
	Status   int
	DurMs    int
	IP       string
}

// AccessLog is the asynchronous batch inserter for api_access_log. Enqueue never blocks the
// request path: it appends to a bounded buffer and drops (incrementing Dropped) on overflow. A
// background loop flushes batches, grouping by tenant so each INSERT runs under that tenant's RLS
// binding (api_access_log is Class T, not platform-owned).
type AccessLog struct {
	store *db.Store
	cap   int

	mu      sync.Mutex
	buf     []AccessRecord
	dropped int64

	stop chan struct{}
	done chan struct{}
}

// NewAccessLog builds a writer with a bounded buffer of capacity cap (min 1).
func NewAccessLog(store *db.Store, capacity int) *AccessLog {
	if capacity < 1 {
		capacity = 1024
	}
	return &AccessLog{store: store, cap: capacity, stop: make(chan struct{}), done: make(chan struct{})}
}

// Enqueue buffers a record without blocking. Records with an empty tenant (pre-auth failures) are
// dropped, since api_access_log requires a tenant binding to insert.
func (a *AccessLog) Enqueue(r AccessRecord) {
	if r.TenantID == "" {
		return
	}
	a.mu.Lock()
	if len(a.buf) >= a.cap {
		a.dropped++
		a.mu.Unlock()
		return
	}
	a.buf = append(a.buf, r)
	a.mu.Unlock()
}

// Dropped returns the number of records dropped due to buffer overflow (for a metric gauge).
func (a *AccessLog) Dropped() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dropped
}

// Flush writes and clears the current buffer, grouping by tenant. Best-effort: a per-tenant
// insert error is swallowed (telemetry must never fail a request or wedge the loop).
func (a *AccessLog) Flush(ctx context.Context) {
	a.mu.Lock()
	batch := a.buf
	a.buf = nil
	a.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	byTenant := map[string][]AccessRecord{}
	for _, r := range batch {
		byTenant[r.TenantID] = append(byTenant[r.TenantID], r)
	}
	for tid, rows := range byTenant {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		_ = a.store.Tx(tctx, func(c *pg.Conn) error {
			for _, r := range rows {
				if err := c.ExecParams(
					`insert into api_access_log (tenant_id, user_id, method, route, status, dur_ms, ip)
					 values ($1,$2,$3,$4,$5,$6,$7)`,
					r.TenantID, nullIf(r.UserID), r.Method, r.Route, r.Status, r.DurMs, nullIf(r.IP)); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

// Start launches the background flush loop at the given interval. Stop ends it after a final flush.
func (a *AccessLog) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		defer close(a.done)
		for {
			select {
			case <-a.stop:
				a.Flush(context.Background())
				return
			case <-t.C:
				a.Flush(context.Background())
			}
		}
	}()
}

// Stop flushes remaining records and terminates the loop.
func (a *AccessLog) Stop() {
	select {
	case <-a.stop:
		return // already stopped
	default:
		close(a.stop)
		<-a.done
	}
}
