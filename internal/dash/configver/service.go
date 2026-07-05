package configver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Auditor is the consumer-side view of the per-tenant hash-chained audit log (satisfied by
// *audit.Log). Publish uses AppendConn to chain its row in the SAME transaction as the pointer
// flip; the non-publish lifecycle writes use Append (their own tx, after the store op).
type Auditor interface {
	Append(ctx context.Context, e audit.Entry) error
	AppendConn(ctx context.Context, c *pg.Conn, e audit.Entry) error
}

var _ Auditor = (*audit.Log)(nil)

// Service is the kind-agnostic config-versioning lifecycle engine (doc 07 §6). It holds one
// Validator per kind (injected by routing / workflows) and an optional OnBump callback fired
// after every epoch bump (wired in cmd/dashboardd to rotation.Engine.Invalidate for pool-affecting
// kinds — closing OI-KEYS-4).
type Service struct {
	store      Store
	audit      Auditor
	validators map[string]Validator
	onBump     func(tenantID, kind, scopeKey string)
	now        func() time.Time
}

// Config bundles the Service's collaborators.
type Config struct {
	Store      Store
	Audit      Auditor
	Validators map[string]Validator                  // kind -> validator
	OnBump     func(tenantID, kind, scopeKey string) // optional epoch-bump callback
	Now        func() time.Time
}

// New builds a Service from cfg, applying defaults.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Validators == nil {
		cfg.Validators = map[string]Validator{}
	}
	return &Service{
		store:      cfg.Store,
		audit:      cfg.Audit,
		validators: cfg.Validators,
		onBump:     cfg.OnBump,
		now:        cfg.Now,
	}
}

// --- draft lifecycle ---

// CreateDraft creates a new draft version for (kind, scopeKey). The scope_key grammar and the
// JSON-object payload shape are enforced here; the parent (default expected_active_version_id at
// publish) is the version active for this scope at draft time.
func (s *Service) CreateDraft(ctx context.Context, kind, scopeKey string, payload json.RawMessage) (Version, error) {
	if !ValidScopeKey(scopeKey) {
		return Version{}, ErrInvalidScopeKey
	}
	if !isJSONObject(payload) {
		return Version{}, ErrInvalidPayload
	}
	parent, _, err := s.store.ActiveVersionID(ctx, kind, scopeKey)
	if err != nil {
		return Version{}, err
	}
	v, err := s.store.CreateDraft(ctx, kind, scopeKey, payload, parent, actorOf(ctx))
	if err != nil {
		return Version{}, err
	}
	_ = s.writeAudit(ctx, "config_draft_create", kind, v.ID, nil, statusSnap(v))
	return v, nil
}

// GetVersion returns one version by id (RLS-scoped).
func (s *Service) GetVersion(ctx context.Context, id string) (Version, error) {
	return s.store.Get(ctx, id)
}

// PatchDraft overwrites a draft/validated version's payload, reverting it to draft and clearing
// the pinned hash (doc 07 §6). A published/archived target -> ErrVersionConflict.
func (s *Service) PatchDraft(ctx context.Context, id string, payload json.RawMessage) (Version, error) {
	if !isJSONObject(payload) {
		return Version{}, ErrInvalidPayload
	}
	v, err := s.store.PatchDraft(ctx, id, payload)
	if err != nil {
		return Version{}, err
	}
	_ = s.writeAudit(ctx, "config_draft_patch", v.Kind, v.ID, nil, statusSnap(v))
	return v, nil
}

// Clone copies a version's payload into a fresh draft (new version number, parent = current
// active). Nothing about the source is mutated.
func (s *Service) Clone(ctx context.Context, id string) (Version, error) {
	src, err := s.store.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	parent, _, err := s.store.ActiveVersionID(ctx, src.Kind, src.ScopeKey)
	if err != nil {
		return Version{}, err
	}
	v, err := s.store.CreateDraft(ctx, src.Kind, src.ScopeKey, src.Payload, parent, actorOf(ctx))
	if err != nil {
		return Version{}, err
	}
	_ = s.writeAudit(ctx, "config_clone", v.Kind, v.ID, statusSnap(src), statusSnap(v))
	return v, nil
}

// Validate runs the kind's Validator over the version payload, stores the report, and — when the
// report has zero error-severity entries — pins payload_hash and transitions the version to
// 'validated' (doc 07 §5/§6). It always succeeds at the transport level; a failed rule is report
// content. Re-validation of a published/archived version is refused (ErrVersionConflict).
func (s *Service) Validate(ctx context.Context, id string) (Version, error) {
	v, err := s.store.Get(ctx, id)
	if err != nil {
		return Version{}, err
	}
	if v.Status == StatusPublished || v.Status == StatusArchived {
		return Version{}, ErrVersionConflict
	}
	report, hasErr, err := s.runValidator(ctx, v.Kind, v.ScopeKey, v.Payload)
	if err != nil {
		return Version{}, err
	}
	status := StatusValidated
	var hash []byte
	if hasErr {
		status = StatusDraft
	} else {
		if hash, err = hashPayload(v.Payload); err != nil {
			return Version{}, err
		}
	}
	stored, err := buildStoredReport(s.now(), hash, report)
	if err != nil {
		return Version{}, err
	}
	out, err := s.store.SaveValidation(ctx, id, stored, hash, status)
	if err != nil {
		return Version{}, err
	}
	_ = s.writeAudit(ctx, "config_validate", out.Kind, out.ID, statusSnap(v), statusSnap(out))
	return out, nil
}

// --- publish / rollback ---

// Publish makes versionID the active config for its scope through the doc 07 §6 serialized
// pointer-flip transaction. expected is the expected_active_version_id (nil defaults to the
// version's parent_version_id). A stale expectation -> ErrVersionConflict.
func (s *Service) Publish(ctx context.Context, versionID string, expected *string) (Version, error) {
	v, err := s.store.Get(ctx, versionID)
	if err != nil {
		return Version{}, err
	}
	return s.publish(ctx, v, expected, false)
}

// Rollback re-publishes a prior version (by version NUMBER) for (kind, scopeKey). Per OI-RW-3 the
// validators are RE-RUN at rollback (the world drifts — a Provider EXCLUDED since that version
// shipped must block it); hard errors -> ErrVersionConflict with the fresh report attached to the
// returned version's ValidationReport-shaped error is surfaced by the handler.
func (s *Service) Rollback(ctx context.Context, kind, scopeKey string, toVersion int, expected *string) (Version, error) {
	v, err := s.store.GetByVersion(ctx, kind, scopeKey, toVersion)
	if err != nil {
		return Version{}, err
	}
	// Re-validate against the current world; hard errors block the rollback.
	_, hasErr, verr := s.runValidator(ctx, v.Kind, v.ScopeKey, v.Payload)
	if verr != nil {
		return Version{}, verr
	}
	if hasErr {
		return Version{}, ErrVersionConflict
	}
	return s.publish(ctx, v, expected, true)
}

// publish is the shared publish/rollback body: it defaults the expectation, extracts the
// workflow_index row for waterfall kinds, runs the store transaction (audit chained in-tx), then
// fires the epoch-bump callback after commit.
func (s *Service) publish(ctx context.Context, v Version, expected *string, rollback bool) (Version, error) {
	exp := v.ParentVersionID
	if expected != nil {
		exp = *expected
	}
	action := "config_publish"
	if rollback {
		action = "config_rollback"
	}
	var idx *workflowIndexRow
	if v.Kind == KindWaterfallWorkflow {
		idx = extractWorkflowIndex(v.Payload)
	}
	params := PublishParams{
		Kind:                    v.Kind,
		ScopeKey:                v.ScopeKey,
		VersionID:               v.ID,
		ExpectedActiveVersionID: exp,
		PublishedBy:             actorOf(ctx),
		Rollback:                rollback,
	}
	aud := func(c *pg.Conn, prevActive string) error {
		return s.audit.AppendConn(ctx, c, audit.Entry{
			Action:      action,
			ObjectKind:  v.Kind,
			ObjectID:    v.ID,
			ActorUserID: actorOf(ctx),
			ActorRole:   roleOf(ctx),
			Before:      prevSnap(prevActive),
			After:       statusSnap(Version{ID: v.ID, Status: StatusPublished, Version: v.Version}),
		})
	}
	out, err := s.store.Publish(ctx, params, idx, aud)
	if err != nil {
		return Version{}, err
	}
	s.fireBump(ctx, v.Kind, v.ScopeKey)
	return out, nil
}

// --- reads ---

// ListVersions returns versions for (kind, scopeKey), newest-first, bounded + cursor-paginated.
func (s *Service) ListVersions(ctx context.Context, kind, scopeKey string, cur db.Cursor, limit int) ([]Version, db.Cursor, error) {
	return s.store.List(ctx, kind, scopeKey, cur, limit)
}

// ActiveVersion returns the currently-published version for (kind, scopeKey), if any.
func (s *Service) ActiveVersion(ctx context.Context, kind, scopeKey string) (Version, bool, error) {
	id, ok, err := s.store.ActiveVersionID(ctx, kind, scopeKey)
	if err != nil || !ok {
		return Version{}, false, err
	}
	v, err := s.store.Get(ctx, id)
	if err != nil {
		return Version{}, false, err
	}
	return v, true, nil
}

// ListWorkflows returns the denormalized workflow_index (the Waterfall list view).
func (s *Service) ListWorkflows(ctx context.Context, cur db.Cursor, limit int) ([]WorkflowRow, db.Cursor, error) {
	return s.store.ListWorkflows(ctx, cur, limit)
}

// Epochs returns the config_epochs counters for the caller's tenant.
func (s *Service) Epochs(ctx context.Context) ([]Epoch, error) {
	return s.store.ListEpochs(ctx)
}

// BumpEpoch increments config_epochs(tenant, kind) and fires the OnBump callback. It is the
// configver-owned sentinel-kind bump API (doc 07 §8.1/§10): providers CRUD (provider_catalog) and
// key-pool writes (key_pool) call it so config_epochs keeps a single write mechanism.
func (s *Service) BumpEpoch(ctx context.Context, kind, scopeKey string) error {
	if err := s.store.BumpEpoch(ctx, kind); err != nil {
		return err
	}
	s.fireBump(ctx, kind, scopeKey)
	return nil
}

func (s *Service) fireBump(ctx context.Context, kind, scopeKey string) {
	if s.onBump == nil {
		return
	}
	tenantID := ""
	if p, err := tenant.FromContext(ctx); err == nil {
		tenantID = p.TenantID
	}
	s.onBump(tenantID, kind, scopeKey)
}

// runValidator runs the kind's Validator and reports whether the returned report has any
// error-severity entries. Missing validator -> ErrNoValidator.
func (s *Service) runValidator(ctx context.Context, kind, scopeKey string, payload json.RawMessage) (report json.RawMessage, hasErr bool, err error) {
	v, ok := s.validators[kind]
	if !ok {
		return nil, false, ErrNoValidator
	}
	report, err = v.Validate(ctx, kind, scopeKey, payload)
	if err != nil {
		return nil, false, err
	}
	var probe struct {
		Errors []json.RawMessage `json:"errors"`
	}
	_ = json.Unmarshal(report, &probe)
	return report, len(probe.Errors) > 0, nil
}

// --- audit + snapshot helpers ---

func (s *Service) writeAudit(ctx context.Context, action, kind, id string, before, after json.RawMessage) error {
	return s.audit.Append(ctx, audit.Entry{
		Action:      action,
		ObjectKind:  kind,
		ObjectID:    id,
		ActorUserID: actorOf(ctx),
		ActorRole:   roleOf(ctx),
		Before:      before,
		After:       after,
	})
}

func statusSnap(v Version) json.RawMessage {
	return jraw(map[string]string{"id": v.ID, "status": v.Status, "version": strconv.Itoa(v.Version)})
}

func prevSnap(prevActive string) json.RawMessage {
	if prevActive == "" {
		return nil
	}
	return jraw(map[string]string{"prev_active_version_id": prevActive})
}

// buildStoredReport assembles the doc 07 §5 stored validation_report:
// {"validated_at","payload_hash":"<hex|">","errors":[...],"warnings":[...]}. It re-embeds the
// validator's raw errors/warnings arrays and stamps the pin + timestamp.
func buildStoredReport(now time.Time, hash []byte, validatorReport json.RawMessage) (json.RawMessage, error) {
	var probe struct {
		Errors   []json.RawMessage `json:"errors"`
		Warnings []json.RawMessage `json:"warnings"`
	}
	if len(validatorReport) > 0 {
		_ = json.Unmarshal(validatorReport, &probe)
	}
	if probe.Errors == nil {
		probe.Errors = []json.RawMessage{}
	}
	if probe.Warnings == nil {
		probe.Warnings = []json.RawMessage{}
	}
	out := map[string]any{
		"validated_at": now.UTC().Format(time.RFC3339Nano),
		"payload_hash": hex.EncodeToString(hash),
		"errors":       probe.Errors,
		"warnings":     probe.Warnings,
	}
	return json.Marshal(out)
}

func jraw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func actorOf(ctx context.Context) string {
	if p, err := tenant.FromContext(ctx); err == nil {
		return p.UserID
	}
	return ""
}

func roleOf(ctx context.Context) string {
	if p, err := tenant.FromContext(ctx); err == nil {
		return db.RoleFromPrincipal(p)
	}
	return ""
}

// extractWorkflowIndex pulls name + trigger from a waterfall_workflow payload for the
// workflow_index denormalization. Missing fields yield empty strings.
func extractWorkflowIndex(payload json.RawMessage) *workflowIndexRow {
	var p struct {
		Name    string `json:"name"`
		Trigger string `json:"trigger"`
	}
	_ = json.Unmarshal(payload, &p)
	return &workflowIndexRow{Name: p.Name, Trigger: p.Trigger}
}
