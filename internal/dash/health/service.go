package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Auditor is the consumer-side view of the per-tenant hash-chained audit log (satisfied by
// *audit.Log). Mutating health actions (schedule PUT, ad-hoc check) append one string-only entry.
type Auditor interface {
	Append(ctx context.Context, e audit.Entry) error
}

var _ Auditor = (*audit.Log)(nil)

// Authenticator resolves a request into a verified Principal (satisfied by httpx.CtxAuthenticator).
// Kept as an interface so this package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// KeyReactivator is the INJECTED seam for auto re-enable probes (brief item 4). health finds
// exhausted/rate_limited Provider Keys and asks the reactivator to probe one; the orchestrator
// implements Probe by delegating to internal/dash/rotation's KM-3 state machine
// (exhausted -> probing -> active). health never imports rotation.
type KeyReactivator interface {
	// Probe drives one Provider Key through a reactivation attempt. A nil return means the key was
	// (or is being) moved back toward active; an error means the probe failed and the key stays put.
	Probe(ctx context.Context, keyID string) error
}

// Deps bundles the collaborators the health surface, scheduler, and reactivator need. Store is the
// shared dual-GUC db.Store; everything else is optional with sensible defaults.
type Deps struct {
	Store       *db.Store
	Audit       Auditor
	Auth        Authenticator
	Resolver    provider.KeyResolver // optional; for the default probe's egress key injection
	Check       CheckFunc            // optional; overrides the default probe (tests inject a fake)
	Reactivator KeyReactivator       // optional; auto re-enable delegate
	Now         func() time.Time
	Logger      *slog.Logger
	Concurrency int           // scheduler worker-pool size (default 4)
	Tick        time.Duration // scheduler tick (default 1s)

	// store lets tests inject a fake Store in place of the PGStore built over Store. Unexported so
	// external wiring always goes through the real db.Store.
	store Store
}

// storeOrPG returns the injected fake Store or a PGStore over the db.Store.
func (d Deps) storeOrPG() Store {
	if d.store != nil {
		return d.store
	}
	return NewPGStore(d.Store)
}

func (d Deps) now() func() time.Time {
	if d.Now != nil {
		return d.Now
	}
	return time.Now
}

func (d Deps) checkFn() CheckFunc {
	if d.Check != nil {
		return d.Check
	}
	return NewProbeCheck(d.Resolver, d.now())
}

// Service is the Provider Health business layer over a Store + Auditor + CheckFunc.
type Service struct {
	store Store
	audit Auditor
	check CheckFunc
	now   func() time.Time
}

// NewService builds the health Service from Deps.
func NewService(d Deps) *Service {
	return &Service{store: d.storeOrPG(), audit: d.Audit, check: d.checkFn(), now: d.now()}
}

// --- reads ---

// ProviderStatuses returns the current health snapshot for every recently-checked Provider.
func (s *Service) ProviderStatuses(ctx context.Context) ([]ProviderStatus, error) {
	return s.store.ProviderStatuses(ctx)
}

// Timeline serves the uptime/latency timeline for a Provider. Day granularity reads folded
// provider_health_1d (up to 90 day-buckets); hour granularity reads raw provider_health_checks
// (last 48h). Empty buckets render no_data (acceptance #4). The summary carries window
// uptime/avg/P95/P99. Windows are clamped to their bucket cap; both series are contiguous.
func (s *Service) Timeline(ctx context.Context, providerID string, from, to time.Time, gran string) (TimelineResult, error) {
	from, to = from.UTC(), to.UTC()
	res := TimelineResult{ProviderID: providerID, Granularity: gran}

	if gran == "hour" {
		to = truncHourUTC(to).Add(time.Hour)
		// Clamp to the last maxHourBuckets hours.
		if min := to.Add(-time.Duration(maxHourBuckets) * time.Hour); from.Before(min) {
			from = min
		}
		rows, err := s.store.HourBuckets(ctx, providerID, from, to)
		if err != nil {
			return TimelineResult{}, err
		}
		res.Buckets = buildHourSeries(from, to, rows)
	} else {
		res.Granularity = "day"
		toDay := truncDayUTC(to).AddDate(0, 0, 1)
		fromDay := truncDayUTC(from)
		if min := toDay.AddDate(0, 0, -maxDayBuckets); fromDay.Before(min) {
			fromDay = min
		}
		rows, err := s.store.DayBuckets(ctx, providerID, fromDay, toDay)
		if err != nil {
			return TimelineResult{}, err
		}
		res.Buckets = buildDaySeries(fromDay, toDay, rows)
		from, to = fromDay, toDay
	}
	res.From, res.To = from, to

	sample, err := s.store.SampleWindow(ctx, providerID, from, to)
	if err != nil {
		return TimelineResult{}, err
	}
	res.Summary = statsFrom(sample)
	return res, nil
}

// Regional aggregates health by region over [from, to).
func (s *Service) Regional(ctx context.Context, from, to time.Time) ([]RegionAgg, error) {
	return s.store.Regional(ctx, from.UTC(), to.UTC())
}

// ListSchedules returns every persisted health schedule.
func (s *Service) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return s.store.ListSchedules(ctx)
}

// --- rollup ---

// FoldDay recomputes the provider_health_1d rows for the given UTC day from raw checks (REPLACE
// semantics — a repair refold recomputes the whole bucket, doc 03 §9.4). Returns providers folded.
func (s *Service) FoldDay(ctx context.Context, dayUTC time.Time) (int, error) {
	return s.store.FoldDay(ctx, truncDayUTC(dayUTC))
}

// --- writes ---

// UpsertSchedule validates and persists one Provider's health schedule, then audits the write.
func (s *Service) UpsertSchedule(ctx context.Context, in Schedule) (Schedule, error) {
	if msg, ok := in.validate(); !ok {
		return Schedule{}, wrapValidation(msg)
	}
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return Schedule{}, err
	}
	in.UpdatedBy = p.UserID
	out, err := s.store.UpsertSchedule(ctx, in)
	if err != nil {
		return Schedule{}, err
	}
	if s.audit != nil {
		_ = s.audit.Append(ctx, audit.Entry{
			Action:      "health_schedule_put",
			ObjectKind:  "health_schedules",
			ObjectID:    out.ProviderID,
			ActorUserID: p.UserID,
			ActorRole:   db.RoleFromPrincipal(p),
			After:       scheduleSnap(out),
		})
	}
	return out, nil
}

// RunCheck triggers one ad-hoc bounded probe for a Provider (POST /health/checks/run), writes the
// result row, and audits it. A Provider with no catalog target is ErrNotFound.
func (s *Service) RunCheck(ctx context.Context, providerID string) (CheckResult, error) {
	if providerID == "" {
		return CheckResult{}, wrapValidation("provider_id is required")
	}
	t, ok, err := s.store.ProviderTarget(ctx, providerID)
	if err != nil {
		return CheckResult{}, err
	}
	if !ok {
		return CheckResult{}, ErrNotFound
	}
	res := s.check(ctx, t)
	if res.Region == "" {
		res.Region = firstRegion(t.Regions)
	}
	if err := s.store.WriteCheck(ctx, providerID, res, s.now()); err != nil {
		return CheckResult{}, err
	}
	if s.audit != nil {
		p, _ := tenant.FromContext(ctx)
		_ = s.audit.Append(ctx, audit.Entry{
			Action:      "health_check_run",
			ObjectKind:  "provider_health_checks",
			ObjectID:    providerID,
			ActorUserID: p.UserID,
			ActorRole:   db.RoleFromPrincipal(p),
			After:       checkSnap(res),
		})
	}
	return res, nil
}

// --- audit snapshot helpers (string-only so the hash chain re-canonicalizes identically) ---

func scheduleSnap(s Schedule) json.RawMessage {
	b, _ := json.Marshal(map[string]string{
		"provider_id": s.ProviderID,
		"interval_s":  itoa(s.IntervalS),
		"jitter_pct":  itoa(s.JitterPct),
		"enabled":     boolStr(s.Enabled),
	})
	return b
}

func checkSnap(r CheckResult) json.RawMessage {
	b, _ := json.Marshal(map[string]string{
		"status":      r.Status,
		"error_class": r.ErrorClass,
		"lat_ms":      itoa(r.LatencyMS),
	})
	return b
}
