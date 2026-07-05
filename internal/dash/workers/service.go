package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Service is module 9's business layer over the registry Store: heartbeat convergence, the
// desired-state actions (all audited, intent-only), scale delegation, and rolling restart.
type Service struct {
	store   *Store
	dbStore *db.Store
	audit   *audit.Log
	scaler  ScaleIntentSetter
	now     func() time.Time
	log     *slog.Logger
	rolling *rollingExec
}

// ServiceConfig wires the Service.
type ServiceConfig struct {
	Store      *db.Store
	Audit      *audit.Log
	Scaler     ScaleIntentSetter // queues.Service (single writer of queue_defs.desired_replicas)
	Now        func() time.Time
	Logger     *slog.Logger
	InstanceID string
}

// NewService assembles the Service and its rolling-restart executor.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	st := NewStore(cfg.Store)
	s := &Service{
		store: st, dbStore: cfg.Store, audit: cfg.Audit, scaler: cfg.Scaler,
		now: cfg.Now, log: cfg.Logger,
	}
	s.rolling = newRollingExec(s, cfg.InstanceID)
	return s
}

// Heartbeat applies a 10s beat and returns the row (the handler echoes desired_state). The raw
// worker_heartbeats + worker_stats_5m fold is best-effort: telemetry must never wedge
// convergence.
func (s *Service) Heartbeat(ctx context.Context, b Beat) (WorkerRow, error) {
	now := s.now()
	row, err := s.store.Upsert(ctx, b, now)
	if err != nil {
		return WorkerRow{}, err
	}
	if err := s.store.RecordBeat(ctx, b, now); err != nil {
		s.log.Warn("record heartbeat telemetry failed", "worker", b.ID, "err", err)
	}
	return row, nil
}

// List / Get / Stats delegate to the store.
func (s *Service) List(ctx context.Context, f WorkerFilter, cur db.Cursor, limit int) ([]WorkerRow, db.Cursor, error) {
	return s.store.List(ctx, f, cur, limit)
}
func (s *Service) Get(ctx context.Context, id string) (WorkerRow, bool, error) {
	return s.store.Get(ctx, id)
}
func (s *Service) Stats(ctx context.Context, id string, from, to time.Time) ([]WorkerStat, error) {
	return s.store.Stats(ctx, id, from, to)
}

// Restart writes desired_state=stopped + a restart marker; the supervisor relaunches (the
// dashboard cannot spawn processes) and the next boot heartbeat resets desired_state=running.
func (s *Service) Restart(ctx context.Context, id string) (WorkerRow, error) {
	return s.act(ctx, id, DesiredStopped, true, "worker_restart")
}

// Drain writes desired_state=draining: finish in-flight Enrichment Jobs (leased keys + reserved
// credits released cleanly), THEN stop — drain ≠ stop (doc 06 §4).
func (s *Service) Drain(ctx context.Context, id string) (WorkerRow, error) {
	return s.act(ctx, id, DesiredDraining, false, "worker_drain")
}

// Pause writes desired_state=paused: stop claiming, process stays up, in-flight jobs finish.
func (s *Service) Pause(ctx context.Context, id string) (WorkerRow, error) {
	return s.act(ctx, id, DesiredPaused, false, "worker_pause")
}

// Resume writes desired_state=running: resume claiming.
func (s *Service) Resume(ctx context.Context, id string) (WorkerRow, error) {
	return s.act(ctx, id, DesiredRunning, false, "worker_resume")
}

func (s *Service) act(ctx context.Context, id, desired string, restart bool, action string) (WorkerRow, error) {
	row, err := s.store.SetDesiredState(ctx, id, desired, restart, s.now())
	if err != nil {
		return WorkerRow{}, err
	}
	s.appendAudit(ctx, action, "workers", id, map[string]any{"desired_state": desired, "restart": restart})
	return row, nil
}

// Scale records worker-count intent for a queue by delegating to the queue_defs single writer
// (doc 06 §5). Intent only — actuation is deploy-layer. A nil scaler is an honest no-op error.
func (s *Service) Scale(ctx context.Context, queue string, replicas int) error {
	if s.scaler == nil {
		return ErrNotFound // no scale-intent writer wired
	}
	if err := s.scaler.SetScaleIntent(ctx, queue, replicas); err != nil {
		return err
	}
	s.appendAudit(ctx, "worker_scale", "queue_defs", queue, map[string]any{"replicas": replicas})
	return nil
}

// RollingRestart starts a staged drain-first restart honoring max_unavailable, as a 202 bulk job.
func (s *Service) RollingRestart(ctx context.Context, kind, queue string, maxUnavailable int) (string, error) {
	return s.rolling.submit(ctx, kind, queue, maxUnavailable)
}

// RollingStatus returns a rolling-restart bulk job's progress (RLS-scoped read).
func (s *Service) RollingStatus(ctx context.Context, id string) (RollingJob, bool, error) {
	return s.rolling.status(ctx, id)
}

// --- helpers ---

func (s *Service) appendAudit(ctx context.Context, action, kind, objectID string, after map[string]any) {
	if s.audit == nil {
		return
	}
	e := audit.Entry{Action: action, ObjectKind: kind, ObjectID: objectID, After: rawJSON(after)}
	if p, err := tenant.FromContext(ctx); err == nil {
		e.ActorUserID = p.UserID
		e.ActorRole = db.RoleFromPrincipal(p)
	}
	if err := s.audit.Append(ctx, e); err != nil {
		s.log.Error("audit append failed", "action", action, "err", err)
	}
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
