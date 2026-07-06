package configver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// PGStore is the production Store over the Class-T config tables (migration 0006). Every method
// runs through db.Store.Tx, so FORCE RLS scopes rows to the caller's Principal tenant.
type PGStore struct {
	store *db.Store
}

// NewPGStore wires a PGStore to the shared db.Store.
func NewPGStore(store *db.Store) *PGStore { return &PGStore{store: store} }

var _ Store = (*PGStore)(nil)

// versionCols is the single source of truth for the config_versions projection order.
const versionCols = `id, tenant_id, kind, scope_key, version, status, payload,
	payload_hash, validation_report, parent_version_id, created_by, created_at,
	published_at, published_by`

func (s *PGStore) CreateDraft(ctx context.Context, kind, scopeKey string, payload json.RawMessage, parentVersionID, createdBy string) (Version, error) {
	pr, err := tenant.FromContext(ctx)
	if err != nil {
		return Version{}, err
	}
	id := newID()
	var out Version
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		vres, err := c.QueryParams(
			`select coalesce(max(version),0)+1 from config_versions
			   where tenant_id=$1 and kind=$2 and scope_key=$3`, pr.TenantID, kind, scopeKey)
		if err != nil {
			return err
		}
		next := 1
		if len(vres.Rows) > 0 && vres.Rows[0][0] != nil {
			next, _ = strconv.Atoi(*vres.Rows[0][0])
		}
		res, err := c.QueryParams(
			`insert into config_versions
			   (id, tenant_id, kind, scope_key, version, status, payload, parent_version_id, created_by)
			 values ($1,$2,$3,$4,$5,'draft',$6::jsonb,$7,$8) returning `+versionCols,
			id, pr.TenantID, kind, scopeKey, next, string(payload),
			nullUUID(parentVersionID), nullUUID(createdBy))
		if err != nil {
			return err
		}
		out = scanVersion(res.Rows[0])
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	return out, nil
}

func (s *PGStore) Get(ctx context.Context, id string) (Version, error) {
	var out Version
	found := false
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		v, ok, err := selectVersionByID(c, id)
		if err != nil {
			return err
		}
		out, found = v, ok
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	if !found {
		return Version{}, ErrNotFound
	}
	return out, nil
}

func (s *PGStore) GetByVersion(ctx context.Context, kind, scopeKey string, version int) (Version, error) {
	var out Version
	found := false
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select `+versionCols+` from config_versions
			   where kind=$1 and scope_key=$2 and version=$3`, kind, scopeKey, version)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			out, found = scanVersion(res.Rows[0]), true
		}
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	if !found {
		return Version{}, ErrNotFound
	}
	return out, nil
}

func (s *PGStore) PatchDraft(ctx context.Context, id string, payload json.RawMessage) (Version, error) {
	var out Version
	found := false
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		cur, ok, err := selectVersionByID(c, id)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		found = true
		if cur.Status == StatusPublished || cur.Status == StatusArchived {
			return ErrVersionConflict
		}
		// Any edit reverts validated -> draft and clears the pin + report (doc 07 §6).
		res, err := c.QueryParams(
			`update config_versions
			   set payload=$2::jsonb, status='draft', payload_hash=NULL, validation_report=NULL
			 where id=$1 returning `+versionCols, id, string(payload))
		if err != nil {
			return err
		}
		out = scanVersion(res.Rows[0])
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	if !found {
		return Version{}, ErrNotFound
	}
	return out, nil
}

func (s *PGStore) SaveValidation(ctx context.Context, id string, report json.RawMessage, hash []byte, status string) (Version, error) {
	var out Version
	found := false
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`update config_versions
			   set validation_report=$2::jsonb, payload_hash=$3::bytea, status=$4
			 where id=$1 and status in ('draft','validated') returning `+versionCols,
			id, string(report), nullBytea(hash), status)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		found = true
		out = scanVersion(res.Rows[0])
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	if !found {
		return Version{}, ErrVersionConflict // absent, or published/archived (not re-validatable)
	}
	return out, nil
}

func (s *PGStore) List(ctx context.Context, kind, scopeKey string, cur db.Cursor, limit int) ([]Version, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var out []Version
	var next db.Cursor
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		var res *pg.Result
		var err error
		if len(cur.K) > 0 && cur.K[0] != "" {
			after, perr := strconv.Atoi(cur.K[0])
			if perr != nil {
				return db.ErrInvalidCursor
			}
			res, err = c.QueryParams(
				`select `+versionCols+` from config_versions
				   where kind=$1 and scope_key=$2 and version < $3
				 order by version desc limit $4`, kind, scopeKey, after, int64(limit+1))
		} else {
			res, err = c.QueryParams(
				`select `+versionCols+` from config_versions
				   where kind=$1 and scope_key=$2
				 order by version desc limit $3`, kind, scopeKey, int64(limit+1))
		}
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{K: []string{strconv.Itoa(scanVersion(rows[limit-1]).Version)}}
			rows = rows[:limit]
		}
		out = make([]Version, 0, len(rows))
		for _, r := range rows {
			out = append(out, scanVersion(r))
		}
		return nil
	})
	if txErr != nil {
		return nil, db.Cursor{}, txErr
	}
	return out, next, nil
}

func (s *PGStore) ActiveVersionID(ctx context.Context, kind, scopeKey string) (string, bool, error) {
	id := ""
	found := false
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select active_version_id from config_active
			   where kind=$1 and scope_key=$2`, kind, scopeKey)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 && res.Rows[0][0] != nil {
			id, found = *res.Rows[0][0], true
		}
		return nil
	})
	return id, found, txErr
}

func (s *PGStore) ListActive(ctx context.Context, kind string, cur db.Cursor, limit int) ([]ActiveEntry, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var out []ActiveEntry
	var next db.Cursor
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		const cols = `a.scope_key, a.active_version_id, v.version, v.status, a.updated_at`
		var res *pg.Result
		var err error
		if cur.ID != "" {
			res, err = c.QueryParams(
				`select `+cols+` from config_active a join config_versions v on v.id=a.active_version_id
				   where a.kind=$1 and a.scope_key > $2 order by a.scope_key asc limit $3`,
				kind, cur.ID, int64(limit+1))
		} else {
			res, err = c.QueryParams(
				`select `+cols+` from config_active a join config_versions v on v.id=a.active_version_id
				   where a.kind=$1 order by a.scope_key asc limit $2`, kind, int64(limit+1))
		}
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{ID: str(rows[limit-1][0])}
			rows = rows[:limit]
		}
		out = make([]ActiveEntry, 0, len(rows))
		for _, r := range rows {
			e := ActiveEntry{
				ScopeKey:        str(r[0]),
				ActiveVersionID: str(r[1]),
				Status:          str(r[3]),
				UpdatedAt:       parseTS(str(r[4])),
			}
			if r[2] != nil {
				e.Version, _ = strconv.Atoi(*r[2])
			}
			out = append(out, e)
		}
		return nil
	})
	if txErr != nil {
		return nil, db.Cursor{}, txErr
	}
	return out, next, nil
}

// Publish runs the doc 07 §6 serialized publish/rollback transaction. See the package doc and the
// step comments for the pointer-lock protocol; concurrent publishers on one scope serialize on the
// config_active row's FOR UPDATE lock, and the loser's stale expected_active_version_id -> 409.
func (s *PGStore) Publish(ctx context.Context, p PublishParams, idx *workflowIndexRow, aud auditFn) (Version, error) {
	pr, err := tenant.FromContext(ctx)
	if err != nil {
		return Version{}, err
	}
	tenantID := pr.TenantID
	var out Version
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		// 1. Load the version to publish; verify the status gate + payload_hash re-check BEFORE
		//    the pointer ever names it, so config_active can never point at a non-validated row.
		v, ok, err := selectVersionByID(c, p.VersionID)
		if err != nil {
			return err
		}
		if !ok || v.Kind != p.Kind || v.ScopeKey != p.ScopeKey {
			return ErrNotFound
		}
		if p.Rollback {
			if (v.Status != StatusArchived && v.Status != StatusPublished) || v.PublishedAt == nil {
				return ErrNotValidated
			}
		} else if v.Status != StatusValidated {
			return ErrNotValidated
		}
		want, herr := hashPayload(v.Payload)
		if herr != nil {
			return herr
		}
		if len(v.PayloadHash) == 0 || !bytes.Equal(v.PayloadHash, want) {
			return ErrHashMismatch
		}

		// 2. Bootstrap the pointer row (first publish only) then lock it — the serialization point.
		insRes, err := c.QueryParams(
			`insert into config_active (tenant_id, kind, scope_key, active_version_id)
			 values ($1,$2,$3,$4) on conflict (tenant_id, kind, scope_key) do nothing
			 returning active_version_id`,
			tenantID, p.Kind, p.ScopeKey, p.VersionID)
		if err != nil {
			return err
		}
		inserted := len(insRes.Rows) > 0

		lockRes, err := c.QueryParams(
			`select active_version_id from config_active
			   where tenant_id=$1 and kind=$2 and scope_key=$3 for update`,
			tenantID, p.Kind, p.ScopeKey)
		if err != nil {
			return err
		}
		if len(lockRes.Rows) == 0 {
			return ErrVersionConflict
		}
		locked := str(lockRes.Rows[0][0])

		prevActive := ""
		if inserted {
			// First-ever publish: no prior active. A non-empty expectation is stale.
			if p.ExpectedActiveVersionID != "" {
				return ErrVersionConflict
			}
			// The pointer already names p.VersionID (verified validated in step 1).
		} else {
			if locked != p.ExpectedActiveVersionID {
				return ErrVersionConflict
			}
			prevActive = locked
			if prevActive != "" && prevActive != p.VersionID {
				if err := c.ExecParams(
					`update config_versions set status='archived' where id=$1 and tenant_id=$2`,
					prevActive, tenantID); err != nil {
					return err
				}
			}
			if err := c.ExecParams(
				`update config_active set active_version_id=$4, updated_at=now()
				   where tenant_id=$1 and kind=$2 and scope_key=$3`,
				tenantID, p.Kind, p.ScopeKey, p.VersionID); err != nil {
				return err
			}
		}

		// 2b. FAULT POINT (test-only, inert in production): the pointer now names p.VersionID but
		// this transaction has NOT committed and the epoch bump (step 5) has not run. A publish-
		// crash chaos test assigns configver.PublishFaultAfterPointer to crash/kill the tx here;
		// because everything runs in one transaction, the crash rolls back the pointer flip
		// atomically, so config_active is never left dangling (doc 13 §7 / OI-TS-5).
		firePublishFault()

		// 3. Flip the version -> published (the pointer is authority; status is bookkeeping).
		if err := c.ExecParams(
			`update config_versions set status='published', published_at=now(), published_by=$2
			   where id=$1 and tenant_id=$3`,
			p.VersionID, nullUUID(p.PublishedBy), tenantID); err != nil {
			return err
		}

		// 4. Maintain the denormalized workflow_index (waterfall_workflow kind only).
		if idx != nil {
			if err := c.ExecParams(
				`insert into workflow_index (tenant_id, scope_key, name, trigger, updated_at)
				 values ($1,$2,$3,$4,now())
				 on conflict (tenant_id, scope_key)
				 do update set name=excluded.name, trigger=excluded.trigger, updated_at=now()`,
				tenantID, p.ScopeKey, idx.Name, nullIf(idx.Trigger)); err != nil {
				return err
			}
		}

		// 5. Bump config_epochs for (tenant, kind), exactly once per publish.
		if err := c.ExecParams(
			`insert into config_epochs (tenant_id, kind, epoch) values ($1,$2,1)
			 on conflict (tenant_id, kind) do update set epoch = config_epochs.epoch + 1`,
			tenantID, p.Kind); err != nil {
			return err
		}

		// 6. Append the audit row on THIS connection (same tx as the pointer flip).
		if aud != nil {
			if err := aud(c, prevActive); err != nil {
				return err
			}
		}

		v2, ok, err := selectVersionByID(c, p.VersionID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		out = v2
		return nil
	})
	if txErr != nil {
		return Version{}, txErr
	}
	return out, nil
}

func (s *PGStore) ListWorkflows(ctx context.Context, cur db.Cursor, limit int) ([]WorkflowRow, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var out []WorkflowRow
	var next db.Cursor
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		var res *pg.Result
		var err error
		if cur.ID != "" {
			res, err = c.QueryParams(
				`select scope_key, name, trigger, updated_at from workflow_index
				   where scope_key > $1 order by scope_key asc limit $2`, cur.ID, int64(limit+1))
		} else {
			res, err = c.QueryParams(
				`select scope_key, name, trigger, updated_at from workflow_index
				 order by scope_key asc limit $1`, int64(limit+1))
		}
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{ID: str(rows[limit-1][0])}
			rows = rows[:limit]
		}
		out = make([]WorkflowRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, WorkflowRow{
				ScopeKey:  str(r[0]),
				Name:      str(r[1]),
				Trigger:   str(r[2]),
				UpdatedAt: parseTS(str(r[3])),
			})
		}
		return nil
	})
	if txErr != nil {
		return nil, db.Cursor{}, txErr
	}
	return out, next, nil
}

func (s *PGStore) ListEpochs(ctx context.Context) ([]Epoch, error) {
	var out []Epoch
	txErr := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select kind, epoch from config_epochs order by kind asc`)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			e := Epoch{Kind: str(r[0])}
			if r[1] != nil {
				e.Epoch, _ = strconv.ParseInt(*r[1], 10, 64)
			}
			out = append(out, e)
		}
		return nil
	})
	return out, txErr
}

func (s *PGStore) BumpEpoch(ctx context.Context, kind string) error {
	pr, err := tenant.FromContext(ctx)
	if err != nil {
		return err
	}
	return s.store.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into config_epochs (tenant_id, kind, epoch) values ($1,$2,1)
			 on conflict (tenant_id, kind) do update set epoch = config_epochs.epoch + 1`,
			pr.TenantID, kind)
	})
}

// --- row scanning + helpers ---

func selectVersionByID(c *pg.Conn, id string) (Version, bool, error) {
	res, err := c.QueryParams(`select `+versionCols+` from config_versions where id=$1`, id)
	if err != nil {
		return Version{}, false, err
	}
	if len(res.Rows) == 0 {
		return Version{}, false, nil
	}
	return scanVersion(res.Rows[0]), true, nil
}

func scanVersion(r []*string) Version {
	v := Version{
		ID:               str(r[0]),
		TenantID:         str(r[1]),
		Kind:             str(r[2]),
		ScopeKey:         str(r[3]),
		Status:           str(r[5]),
		Payload:          rawj(r[6]),
		PayloadHash:      decodeByteaOrNil(r[7]),
		ValidationReport: rawj(r[8]),
		ParentVersionID:  str(r[9]),
		CreatedBy:        str(r[10]),
		CreatedAt:        parseTS(str(r[11])),
		PublishedBy:      str(r[13]),
	}
	if r[4] != nil {
		v.Version, _ = strconv.Atoi(*r[4])
	}
	if r[12] != nil && *r[12] != "" {
		t := parseTS(*r[12])
		v.PublishedAt = &t
	}
	return v
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func rawj(p *string) json.RawMessage {
	if p == nil || *p == "" {
		return nil
	}
	return json.RawMessage(*p)
}

// nullIf returns nil for an empty string so a text column is written NULL.
func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullUUID returns nil for an empty string so a uuid column is written NULL.
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullBytea encodes a []byte as \x hex, or NULL when empty (payload_hash cleared).
func nullBytea(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return `\x` + hex.EncodeToString(b)
}

func decodeByteaOrNil(p *string) []byte {
	if p == nil || *p == "" {
		return nil
	}
	s := *p
	if len(s) >= 2 && s[0] == '\\' && (s[1] == 'x' || s[1] == 'X') {
		if b, err := hex.DecodeString(s[2:]); err == nil {
			return b
		}
	}
	return nil
}

func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05.999999Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
