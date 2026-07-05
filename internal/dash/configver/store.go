package configver

import (
	"context"
	"encoding/json"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// Store is the persistence seam the Service depends on (consumer-side interface, satisfied by
// PGStore over the Class-T config tables). Every method binds the caller's Principal via
// db.Store.Tx, so RLS scopes rows to the caller's tenant.
type Store interface {
	// CreateDraft inserts a new draft version, computing the next version number for
	// (tenant, kind, scope_key). parentVersionID captures the active version at draft time
	// (the default expected_active_version_id at publish); "" when none.
	CreateDraft(ctx context.Context, kind, scopeKey string, payload json.RawMessage, parentVersionID, createdBy string) (Version, error)

	// Get returns one version by id (RLS-scoped). ErrNotFound when absent.
	Get(ctx context.Context, id string) (Version, error)

	// GetByVersion returns the version numbered `version` within (kind, scope_key) — the rollback
	// target lookup (rollback names a version NUMBER). ErrNotFound when absent.
	GetByVersion(ctx context.Context, kind, scopeKey string, version int) (Version, error)

	// PatchDraft overwrites a draft/validated version's payload, reverting it to 'draft' and
	// clearing payload_hash + validation_report (doc 07 §6). A published/archived target yields
	// ErrVersionConflict.
	PatchDraft(ctx context.Context, id string, payload json.RawMessage) (Version, error)

	// SaveValidation stores the validation report + pins payload_hash (nil when errors present)
	// and sets status ('validated' on success, 'draft' when the report has errors).
	SaveValidation(ctx context.Context, id string, report json.RawMessage, hash []byte, status string) (Version, error)

	// List returns versions for (kind, scope_key), newest-first (keyset on version DESC), bounded.
	List(ctx context.Context, kind, scopeKey string, cur db.Cursor, limit int) ([]Version, db.Cursor, error)

	// ActiveVersionID returns the config_active pointer for (kind, scope_key), if any.
	ActiveVersionID(ctx context.Context, kind, scopeKey string) (string, bool, error)

	// ListActive returns the active-config scope list for a kind (config_active joined to the
	// active version's number/status), keyset on scope_key, bounded.
	ListActive(ctx context.Context, kind string, cur db.Cursor, limit int) ([]ActiveEntry, db.Cursor, error)

	// Publish runs the serialized publish/rollback transaction (doc 07 §6). It appends the audit
	// row via aud on the SAME connection. idx, when non-nil, upserts workflow_index in-tx.
	Publish(ctx context.Context, p PublishParams, idx *workflowIndexRow, aud auditFn) (Version, error)

	// ListWorkflows returns the denormalized workflow_index list (keyset on scope_key), bounded.
	ListWorkflows(ctx context.Context, cur db.Cursor, limit int) ([]WorkflowRow, db.Cursor, error)

	// ListEpochs returns the config_epochs counters for the caller's tenant.
	ListEpochs(ctx context.Context) ([]Epoch, error)

	// BumpEpoch increments config_epochs(tenantID, kind) (INSERT ... ON CONFLICT DO UPDATE). It
	// is the standalone sentinel-kind bump path (provider_catalog / key_pool); the publish tx
	// bumps inline. tenantID is bound as the Principal tenant by the caller's ctx.
	BumpEpoch(ctx context.Context, kind string) error
}

// workflowIndexRow is the denormalized name/trigger upserted into workflow_index on publish of a
// waterfall_workflow version.
type workflowIndexRow struct {
	Name    string
	Trigger string
}
