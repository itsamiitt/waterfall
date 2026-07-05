package keys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/enrichment/waterfall/internal/dash/audit"
	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/secrets"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Batch terminal statuses (key_import_batches.status; the async lifecycle of doc 04 §4.1).
const (
	StatusImportRunning   = "running"
	StatusImportSucceeded = "succeeded"
	StatusImportPartial   = "partial"
	StatusImportFailed    = "failed"
)

// Sentinel errors the HTTP layer maps to uniform error bodies. None carries key material.
var (
	ErrProviderNotFound  = errors.New("keys: provider not found")
	ErrKeyNotFound       = errors.New("keys: provider key not found")
	ErrPoolNotFound      = errors.New("keys: key pool not found")
	ErrInvalidTransition = errors.New("keys: illegal key state transition")
	ErrInvalidStrategy   = errors.New("keys: unknown pool strategy")
	ErrValidation        = errors.New("keys: validation failed")
)

// DuplicateError reports import/create material that duplicates an existing key by keyed
// fingerprint. It carries the existing key id (never the plaintext).
type DuplicateError struct{ ExistingID string }

func (e *DuplicateError) Error() string { return "keys: duplicate of key " + e.ExistingID }

// Service is module 3's business layer: metadata CRUD, KM-3 state actions, pools, and the async
// import pipeline. It seals every secret through secrets.Backend and audits every mutation with
// redacted (string/enum only) snapshots.
type Service struct {
	store   *pgStore
	secrets secrets.Backend
	audit   *audit.Log
	log     *slog.Logger
	now     func() time.Time
	bulk    *bulkRegistry
}

// NewService wires the service over the shared db.Store, envelope backend, and audit chain.
func NewService(store *db.Store, backend secrets.Backend, auditLog *audit.Log, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:   newPGStore(store),
		secrets: backend,
		audit:   auditLog,
		log:     logger,
		now:     time.Now,
		bulk:    newBulkRegistry(),
	}
}

// CreateKeyInput is the service-facing create request (doc 04 §2.4). Secret holds plaintext only
// transiently; it is sealed and discarded here.
type CreateKeyInput struct {
	Label, Secret, AuthMethod, Region, Environment, Team, Owner, Notes, RotationGroup, ExpiresAt string
	Weight, Priority, DailyLimit, MonthlyLimit, RPMLimit, ConcurrencyLimit                       *int64
	Tags                                                                                         []string
	PoolIDs                                                                                      []string
	OwnerTenantID                                                                                string
}

// CreateKey seals the secret and inserts a provider_keys row. It returns the created Key and the
// display fingerprint prefix. Duplicate material (by keyed fingerprint) is rejected without
// persisting a key, and its orphan envelope is removed.
func (svc *Service) CreateKey(ctx context.Context, providerID string, in CreateKeyInput) (Key, string, error) {
	if in.Secret == "" {
		return Key{}, "", fmt.Errorf("%w: secret is required", ErrValidation)
	}
	ok, err := svc.store.providerExists(ctx, providerID)
	if err != nil {
		return Key{}, "", err
	}
	if !ok {
		return Key{}, "", ErrProviderNotFound
	}

	envID, err := svc.secrets.Seal(ctx, "provider_key", []byte(in.Secret))
	if err != nil {
		return Key{}, "", err
	}
	if dup, found, derr := svc.store.fingerprintDup(ctx, providerID, string(envID)); derr != nil {
		return Key{}, "", derr
	} else if found {
		_ = svc.store.deleteEnvelope(ctx, string(envID))
		return Key{}, "", &DuplicateError{ExistingID: dup}
	}

	k := Key{
		ID:               newID(),
		ProviderID:       providerID,
		Label:            sanitizeCell(in.Label),
		SecretEnvelopeID: string(envID),
		SecretLast4:      last4(in.Secret),
		AuthMethod:       in.AuthMethod,
		Status:           StatusActive,
		Weight:           weightOr(in.Weight),
		Priority:         in.Priority,
		Region:           sanitizeCell(in.Region),
		Environment:      sanitizeCell(in.Environment),
		Team:             sanitizeCell(in.Team),
		Owner:            sanitizeCell(in.Owner),
		Notes:            sanitizeCell(in.Notes),
		DailyLimit:       in.DailyLimit,
		MonthlyLimit:     in.MonthlyLimit,
		RPMLimit:         in.RPMLimit,
		ConcurrencyLimit: in.ConcurrencyLimit,
		ExpiresAt:        in.ExpiresAt,
		OwnerTenantID:    in.OwnerTenantID,
		RotationGroup:    in.RotationGroup,
		Tags:             sanitizeTags(in.Tags),
		CreatedBy:        actorFrom(ctx),
	}
	if err := svc.store.insertKey(ctx, k); err != nil {
		_ = svc.store.deleteEnvelope(ctx, string(envID))
		return Key{}, "", err
	}
	if len(in.PoolIDs) > 0 {
		if err := svc.store.addKeyToPools(ctx, k.ID, in.PoolIDs); err != nil {
			svc.log.Error("create key: pool membership failed", "key", k.ID, "err", err)
		}
	}

	prefix, _ := svc.store.fingerprintPrefix(ctx, string(envID))
	fresh, _, _ := svc.store.getKey(ctx, k.ID)
	svc.appendAudit(ctx, "key_create", "provider_keys", k.ID, nil, keySnapshot(fresh))
	return fresh, prefix, nil
}

// GetKey returns a key's metadata + computed availability, or ErrKeyNotFound.
func (svc *Service) GetKey(ctx context.Context, id string) (Key, string, error) {
	k, ok, err := svc.store.getKey(ctx, id)
	if err != nil {
		return Key{}, "", err
	}
	if !ok {
		return Key{}, "", ErrKeyNotFound
	}
	prefix, _ := svc.store.fingerprintPrefix(ctx, k.SecretEnvelopeID)
	return k, prefix, nil
}

// ListKeys returns a bounded, cursor-paginated page under the given filter.
func (svc *Service) ListKeys(ctx context.Context, f KeyFilter, cur db.Cursor, limit int) ([]Key, db.Cursor, error) {
	return svc.store.listKeys(ctx, f, cur, limit)
}

// PatchKey applies a metadata patch (never ciphertext) and audits the change.
func (svc *Service) PatchKey(ctx context.Context, id string, p KeyPatch) (Key, error) {
	before, ok, err := svc.store.getKey(ctx, id)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	sanitizePatch(&p)
	k, ok, err := svc.store.updateKeyMeta(ctx, id, p)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	svc.appendAudit(ctx, "key_update", "provider_keys", id, keySnapshot(before), keySnapshot(k))
	return k, nil
}

// transition applies a guarded status action (enable/disable/archive) and audits it.
func (svc *Service) transition(ctx context.Context, action, id, reason, auditAction string) (Key, error) {
	before, ok, err := svc.store.getKey(ctx, id)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	to, ok := transitionTarget(action, before.Status)
	if !ok {
		return Key{}, ErrInvalidTransition
	}
	k, ok, err := svc.store.setStatus(ctx, id, to, reason)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	svc.appendAudit(ctx, auditAction, "provider_keys", id, keySnapshot(before), keySnapshot(k))
	return k, nil
}

// EnableKey / DisableKey / ArchiveKey are the manual KM-3 transitions.
func (svc *Service) EnableKey(ctx context.Context, id string) (Key, error) {
	return svc.transition(ctx, "enable", id, "", "key_enable")
}
func (svc *Service) DisableKey(ctx context.Context, id, reason string) (Key, error) {
	return svc.transition(ctx, "disable", id, reason, "key_disable")
}
func (svc *Service) ArchiveKey(ctx context.Context, id string) (Key, error) {
	return svc.transition(ctx, "archive", id, "", "key_archive")
}

// RotateResult is the POST /keys/{id}/rotate response shape.
type RotateResult struct {
	SuccessorKeyID string
	OldKeyID       string
	OldKeyStatus   string
	OverlapUntil   string
}

// RotateKey seals new material as a successor Key and opens the overlap window (KM-3). overlapS=0
// is the compromise path: the old Key archives immediately.
func (svc *Service) RotateKey(ctx context.Context, id, newSecret string, overlapS int) (RotateResult, error) {
	if newSecret == "" {
		return RotateResult{}, fmt.Errorf("%w: secret is required", ErrValidation)
	}
	old, ok, err := svc.store.getKey(ctx, id)
	if err != nil {
		return RotateResult{}, err
	}
	if !ok {
		return RotateResult{}, ErrKeyNotFound
	}
	if _, ok := transitionTarget("rotate", old.Status); !ok {
		return RotateResult{}, ErrInvalidTransition
	}

	envID, err := svc.secrets.Seal(ctx, "provider_key", []byte(newSecret))
	if err != nil {
		return RotateResult{}, err
	}
	successor := Key{
		ID:               newID(),
		ProviderID:       old.ProviderID,
		Label:            old.Label,
		SecretEnvelopeID: string(envID),
		SecretLast4:      last4(newSecret),
		AuthMethod:       old.AuthMethod,
		Status:           StatusActive,
		Weight:           old.Weight,
		Priority:         old.Priority,
		Region:           old.Region,
		Environment:      old.Environment,
		Team:             old.Team,
		Owner:            old.Owner,
		DailyLimit:       old.DailyLimit,
		MonthlyLimit:     old.MonthlyLimit,
		RPMLimit:         old.RPMLimit,
		ConcurrencyLimit: old.ConcurrencyLimit,
		OwnerTenantID:    old.OwnerTenantID,
		RotationGroup:    old.RotationGroup,
		CreatedBy:        actorFrom(ctx),
	}
	overlapUntil := ""
	oldStatus := StatusArchived
	if overlapS > 0 {
		overlapUntil = svc.now().UTC().Add(time.Duration(overlapS) * time.Second).Format(time.RFC3339)
		oldStatus = StatusRotating
	}
	if err := svc.store.rotateKey(ctx, id, successor, overlapUntil); err != nil {
		_ = svc.store.deleteEnvelope(ctx, string(envID))
		return RotateResult{}, err
	}
	res := RotateResult{SuccessorKeyID: successor.ID, OldKeyID: id, OldKeyStatus: oldStatus, OverlapUntil: overlapUntil}
	svc.appendAudit(ctx, "key_rotate", "provider_keys", id, keySnapshot(old), map[string]string{
		"successor_key_id": successor.ID, "old_key_status": oldStatus, "overlap_until": overlapUntil,
	})
	return res, nil
}

// TestKey / HealthCheckKey / RefreshCredits are metadata-touch actions for P1. Live provider
// egress (provider.Call for test/benchmark, credit sync for refresh) is wired by the P2 rotation
// engine + providers module; here they stamp the relevant timestamp and audit the intent, so the
// endpoints exist and are exercised end to end without reaching a Provider (OI-KEYS-2).
func (svc *Service) TestKey(ctx context.Context, id string) (Key, error) {
	return svc.touch(ctx, id, "last_used_at", "key_test")
}
func (svc *Service) HealthCheckKey(ctx context.Context, id string) (Key, error) {
	return svc.touch(ctx, id, "last_health_at", "key_health_check")
}
func (svc *Service) RefreshCredits(ctx context.Context, id string) (Key, error) {
	return svc.touch(ctx, id, "credits_synced_at", "key_refresh_credits")
}

func (svc *Service) touch(ctx context.Context, id, col, action string) (Key, error) {
	k, ok, err := svc.store.touchKey(ctx, id, col)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrKeyNotFound
	}
	svc.appendAudit(ctx, action, "provider_keys", id, nil, keySnapshot(k))
	return k, nil
}

// UsageSeries is the GET /keys/{id}/usage response. key_usage_* rollups land in P4; for P1 this
// returns the live provider_keys counters plus an empty series and a note (OI-KEYS-3).
type UsageSeries struct {
	KeyID            string
	CreditsUsed      *int64
	CreditsRemaining *int64
	Series           []any
	Note             string
}

func (svc *Service) KeyUsage(ctx context.Context, id string) (UsageSeries, error) {
	k, ok, err := svc.store.getKey(ctx, id)
	if err != nil {
		return UsageSeries{}, err
	}
	if !ok {
		return UsageSeries{}, ErrKeyNotFound
	}
	return UsageSeries{
		KeyID:            k.ID,
		CreditsUsed:      k.CreditsUsed,
		CreditsRemaining: k.CreditsRemaining,
		Series:           []any{},
		Note:             "per-key usage rollups (key_usage_*) arrive in P4; live counters shown",
	}, nil
}

// StartImport validates + parses synchronously (so a bad file / zip bomb / row-cap breach is a
// synchronous 4xx), creates the batch, and launches async processing on a DETACHED context
// carrying the captured Principal. It returns the batch id (the async job id).
func (svc *Service) StartImport(ctx context.Context, providerID, source string, data []byte, ownerTenant string) (string, error) {
	ok, err := svc.store.providerExists(ctx, providerID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrProviderNotFound
	}
	rows, err := parseRows(source, data)
	if err != nil {
		return "", err
	}

	batchID := newID()
	batch := ImportBatch{
		ID: batchID, ProviderID: providerID, Source: source,
		Total: len(rows), Status: StatusImportRunning, CreatedBy: actorFrom(ctx),
	}
	if err := svc.store.insertBatch(ctx, batch); err != nil {
		return "", err
	}
	svc.appendAudit(ctx, "key_import", "key_import_batches", batchID, nil, map[string]string{
		"provider_id": providerID, "source": source, "total": fmt.Sprint(len(rows)),
	})

	bgCtx := detach(ctx)
	go svc.runImport(bgCtx, batchID, providerID, ownerTenant, rows)
	return batchID, nil
}

// ImportStatus returns a batch's progress/results (GET /key-imports/{job_id}).
func (svc *Service) ImportStatus(ctx context.Context, batchID string) (ImportBatch, error) {
	b, ok, err := svc.store.getBatch(ctx, batchID)
	if err != nil {
		return ImportBatch{}, err
	}
	if !ok {
		return ImportBatch{}, ErrKeyNotFound
	}
	return b, nil
}

// --- audit + context helpers ---

// appendAudit writes one redacted audit row. Snapshots MUST be string/int/bool maps only so the
// hash chain re-canonicalizes identically after a jsonb round-trip (doc 05 §8.1). Failure is
// logged, not fatal (matching the httpx audited wrapper's P0 behavior).
func (svc *Service) appendAudit(ctx context.Context, action, kind, objectID string, before, after any) {
	if svc.audit == nil {
		return
	}
	e := audit.Entry{
		Action: action, ObjectKind: kind, ObjectID: objectID,
		Before: rawJSON(before), After: rawJSON(after),
	}
	if p, err := tenant.FromContext(ctx); err == nil {
		e.ActorUserID = p.UserID
		e.ActorRole = db.RoleFromPrincipal(p)
	}
	if err := svc.audit.Append(ctx, e); err != nil {
		svc.log.Error("audit append failed", "action", action, "kind", kind, "err", err)
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

// keySnapshot is the redacted audit image of a Key: identity + status + display fields only,
// never ciphertext and never the full secret (last4 is display-safe, doc 05 §7.3).
func keySnapshot(k Key) map[string]string {
	return map[string]string{
		"id":                 k.ID,
		"provider_id":        k.ProviderID,
		"label":              k.Label,
		"status":             k.Status,
		"health":             k.Health,
		"secret_last4":       k.SecretLast4,
		"secret_envelope_id": k.SecretEnvelopeID,
		"region":             k.Region,
		"environment":        k.Environment,
		"owner_tenant_id":    k.OwnerTenantID,
	}
}

// actorFrom returns the acting user id from the ctx Principal, or "".
func actorFrom(ctx context.Context) string {
	if p, err := tenant.FromContext(ctx); err == nil {
		return p.UserID
	}
	return ""
}

// detach returns a background context carrying the request's Principal, so an async goroutine
// keeps its identity (for the audit chain) after the request context is cancelled.
func detach(ctx context.Context) context.Context {
	if p, err := tenant.FromContext(ctx); err == nil {
		return tenant.WithPrincipal(context.Background(), p)
	}
	return context.Background()
}

// sanitizeTags neutralizes each tag against formula injection.
func sanitizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = sanitizeCell(t)
	}
	return out
}

// sanitizePatch neutralizes patchable text fields.
func sanitizePatch(p *KeyPatch) {
	esc := func(s *string) {
		if s != nil {
			v := sanitizeCell(*s)
			*s = v
		}
	}
	esc(p.Label)
	esc(p.Region)
	esc(p.Environment)
	esc(p.Team)
	esc(p.Owner)
	esc(p.Notes)
	esc(p.RotationGroup)
	if p.Tags != nil {
		*p.Tags = sanitizeTags(*p.Tags)
	}
}

// --- in-process bulk job registry (P1) ---

// bulkRegistry is the P1 in-process bulk-job store (mirrors the D-P0-2 in-process idempotency
// ledger): POST /keys/bulk executes inline and records a result here, which GET /bulk-jobs/{id}
// serves. The durable lease/janitor bulk_jobs model lands in P5 (OI-KEYS-1).
type bulkRegistry struct {
	mu   sync.Mutex
	jobs map[string]*BulkJob
}

func newBulkRegistry() *bulkRegistry { return &bulkRegistry{jobs: map[string]*BulkJob{}} }

// BulkJob is a completed inline bulk operation's record.
type BulkJob struct {
	ID                 string     `json:"job_id"`
	Kind               string     `json:"kind"`
	Op                 string     `json:"op"`
	Status             string     `json:"status"`
	Total              int        `json:"total"`
	Succeeded          int        `json:"succeeded"`
	Failed             int        `json:"failed"`
	MatchedAtExecution int        `json:"matched_at_execution"`
	Errors             []rowError `json:"errors"`
}

func (r *bulkRegistry) put(j *BulkJob) {
	r.mu.Lock()
	r.jobs[j.ID] = j
	r.mu.Unlock()
}

func (r *bulkRegistry) get(id string) (*BulkJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}
