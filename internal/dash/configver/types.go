// Package configver is the dashboard's shared config-versioning lifecycle engine (doc 07 §6,
// doc 03 §2.3): the single draft -> validated -> published -> archived path that Request
// Routing (module 6) and Waterfall Configuration (module 7) both ride. It is KIND-AGNOSTIC —
// routing_policy and waterfall_workflow differ only in the injected Validator and the dry-run
// projection — so there is one publish path, one config_active pointer, one epoch counter, and
// one audit story.
//
// Gates / invariants:
//   - G1 tenant isolation. config_versions / config_active / config_epochs / workflow_index are
//     Class T (FORCE RLS); every method runs through db.Store.Tx bound to the caller's Principal.
//   - Publish serialization (doc 07 §6). Publish locks the config_active POINTER row
//     (INSERT ... ON CONFLICT DO NOTHING then SELECT ... FOR UPDATE) as the single
//     serialization point; a stale expected_active_version_id -> 409 version_conflict. Exactly
//     one of two concurrent publishers on a scope commits.
//   - Reversibility. Rollback is a publish of a prior version id; nothing is ever destroyed.
//   - Config can never override G3/G4. The injected Validator rejects any payload attempting to
//     exceed engine caps; configver itself only enforces the lifecycle.
package configver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// Kind is the config_versions.kind vocabulary (migration 0006 CHECK). configver is agnostic to
// which kind it services; the caller (routing / workflows) picks one.
const (
	KindRoutingPolicy     = "routing_policy"
	KindWaterfallWorkflow = "waterfall_workflow"
	KindAlertRuleset      = "alert_ruleset"
)

// Status is the config_versions.status lifecycle (migration 0006 CHECK; doc 07 §6 stateDiagram).
const (
	StatusDraft     = "draft"
	StatusValidated = "validated"
	StatusPublished = "published"
	StatusArchived  = "archived"
)

// Sentinel errors (wrapped with %w by callers; the HTTP layer maps them to status codes). None
// carries payload content or cross-tenant existence.
var (
	// ErrNotFound reports no version for the id in the caller's tenant scope.
	ErrNotFound = errors.New("configver: version not found")
	// ErrVersionConflict reports the doc 07 §6 pointer staleness / immutable-status conflict
	// (HTTP 409 version_conflict): a PATCH on a published/archived version, or a publish whose
	// expected_active_version_id no longer matches the locked config_active pointer.
	ErrVersionConflict = errors.New("configver: version conflict")
	// ErrNotValidated reports a publish attempt against a version that is not status='validated'
	// (or, for rollback, not in archived/published) — the version must be (re-)validated first.
	ErrNotValidated = errors.New("configver: version is not validated")
	// ErrHashMismatch reports the publish-time payload_hash re-check failed (the pinned hash does
	// not match the stored payload). It should be unreachable when the lifecycle holds.
	ErrHashMismatch = errors.New("configver: payload hash mismatch")
	// ErrInvalidScopeKey reports a malformed scope_key at draft creation (HTTP 400 invalid_scope_key).
	ErrInvalidScopeKey = errors.New("configver: invalid scope_key")
	// ErrInvalidPayload reports a payload that is not a JSON object.
	ErrInvalidPayload = errors.New("configver: payload is not a JSON object")
	// ErrNoValidator reports that no Validator is registered for the kind.
	ErrNoValidator = errors.New("configver: no validator for kind")
)

// Version is the typed read model of one config_versions row (migration 0006). ParentVersionID
// and PublishedBy are "" when NULL; PublishedAt is nil when NULL.
type Version struct {
	ID               string
	TenantID         string
	Kind             string
	ScopeKey         string
	Version          int
	Status           string
	Payload          json.RawMessage
	PayloadHash      []byte
	ValidationReport json.RawMessage
	ParentVersionID  string
	CreatedBy        string
	CreatedAt        time.Time
	PublishedAt      *time.Time
	PublishedBy      string
}

// WorkflowRow is one denormalized workflow_index entry (the Waterfall list view).
type WorkflowRow struct {
	ScopeKey  string    `json:"scope_key"`
	Name      string    `json:"name"`
	Trigger   string    `json:"trigger"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Epoch is one config_epochs counter row.
type Epoch struct {
	Kind  string `json:"kind"`
	Epoch int64  `json:"epoch"`
}

// ActiveEntry is one row of the active-config scope list (config_active joined to its version).
type ActiveEntry struct {
	ScopeKey        string    `json:"scope_key"`
	ActiveVersionID string    `json:"active_version_id"`
	Version         int       `json:"version"`
	Status          string    `json:"status"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Validator is the per-kind validation seam injected into the Service (doc 07 §5). It runs the
// kind's VR catalog over payload and returns the machine-readable report as raw JSON
// ({"errors":[...],"warnings":[...]}); the Service pins payload_hash and stores the augmented
// report. A non-nil err is an internal fault (e.g. a provider lookup failed) — a FAILED RULE is
// report content, never an error (doc 07 §5: validate always returns HTTP 200).
type Validator interface {
	Validate(ctx context.Context, kind, scopeKey string, payload json.RawMessage) (report json.RawMessage, err error)
}

// PublishParams is one publish/rollback request against the config_active pointer (doc 07 §6).
type PublishParams struct {
	Kind                    string
	ScopeKey                string
	VersionID               string // the version being made active
	ExpectedActiveVersionID string // pointer staleness guard; "" means "expect no prior active"
	PublishedBy             string
	Rollback                bool // true => status gate is archived/published (a prior version)
}

// auditFn appends the publish audit row on the same connection as the pointer flip.
type auditFn func(c *pg.Conn, prevActiveID string) error
