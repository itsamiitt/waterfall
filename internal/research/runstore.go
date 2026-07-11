package research

import (
	"context"

	"github.com/enrichment/waterfall/internal/pg"
)

// Run lifecycle states for research_runs (migration 0015). A run moves queued → running → done|failed.
const (
	RunQueued  = "queued"
	RunRunning = "running"
	RunDone    = "done"
	RunFailed  = "failed"
)

// Run is one research run's lifecycle record (research_runs). It is the foundation of the async
// POST /v1/research (202 + run_id) flow and GET /v1/research/{id}: a submission records a queued Run, a
// worker transitions it, and the status endpoint reads it back. Tenant-scoped by RLS like the rest of the
// store — the tenant is never passed as an argument.
type Run struct {
	RunID         string `json:"run_id"`
	SubjectKey    string `json:"subject_key"`
	Status        string `json:"status"`
	ConfigVersion string `json:"config_version"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// CreateRun records a new research run in the queued state, idempotent per (tenant, run_id): a repeated
// submission with the same run_id is a no-op (returns created=false), so a retried POST /v1/research
// returns the existing run rather than starting a second assembly (G2). tenant_id comes from the GUC, so
// RLS WITH CHECK confines the row to the caller's tenant.
func (s *Store) CreateRun(ctx context.Context, runID, subjectKey, configVersion string) (bool, error) {
	created := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`insert into research_runs (tenant_id, run_id, subject_key, status, config_version)
			values (current_setting('app.current_tenant'), $1, $2, 'queued', $3)
			on conflict (tenant_id, run_id) do nothing
			returning id`, runID, subjectKey, configVersion)
		if err != nil {
			return err
		}
		created = len(res.Rows) > 0
		return nil
	})
	return created, err
}

// SetRunStatus transitions a run's status (running/done/failed) and bumps updated_at, within the tenant.
func (s *Store) SetRunStatus(ctx context.Context, runID, status string) error {
	return s.tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update research_runs set status = $2, updated_at = now() where run_id = $1`,
			runID, status)
	})
}

// GetRun returns a run's lifecycle record by run_id within the caller's tenant.
func (s *Store) GetRun(ctx context.Context, runID string) (Run, bool, error) {
	var out Run
	found := false
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select run_id, subject_key, status, config_version, created_at, updated_at
			from research_runs where run_id = $1`, runID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		out = scanRun(res.Rows[0])
		found = true
		return nil
	})
	return out, found, err
}

// ListRuns returns recent research runs within the caller's tenant (newest first, capped).
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []Run
	err := s.tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select run_id, subject_key, status, config_version, created_at, updated_at
			from research_runs order by created_at desc, run_id asc limit $1`, limit)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			out = append(out, scanRun(row))
		}
		return nil
	})
	return out, err
}

func scanRun(row []*string) Run {
	return Run{
		RunID:         rowStr(row, 0),
		SubjectKey:    rowStr(row, 1),
		Status:        rowStr(row, 2),
		ConfigVersion: rowStr(row, 3),
		CreatedAt:     rowStr(row, 4),
		UpdatedAt:     rowStr(row, 5),
	}
}

func rowStr(row []*string, i int) string {
	if i < len(row) && row[i] != nil {
		return *row[i]
	}
	return ""
}
