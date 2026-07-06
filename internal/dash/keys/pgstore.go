package keys

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
)

// newID mints an RFC 4122 v4 uuid from crypto/rand (stdlib only), matching the shape the schema's
// uuid PKs expect.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// --- providers (read-only guard) ---

func (st *pgStore) providerExists(ctx context.Context, id string) (bool, error) {
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select 1 from providers where id = $1`, id)
		if err != nil {
			return err
		}
		found = len(res.Rows) > 0
		return nil
	})
	return found, err
}

// --- provider_keys ---

// insertKey writes one provider_keys row (created_at/updated_at default to now()). It is the
// single INSERT path shared by create and import.
func (st *pgStore) insertKey(ctx context.Context, k Key) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error { return insertKeyTx(c, k) })
}

// insertKeyTx is the INSERT body, reusable inside a larger transaction (rotate).
func insertKeyTx(c *pg.Conn, k Key) error {
	status := k.Status
	if status == "" {
		status = StatusActive
	}
	return c.ExecParams(
		`insert into provider_keys
		   (id, provider_id, label, secret_envelope_id, secret_last4, auth_method, status,
		    weight, priority, region, environment, team, owner, notes,
		    daily_limit, monthly_limit, rpm_limit, concurrency_limit,
		    expires_at, owner_tenant_id, rotation_group, imported_batch_id, tags, created_by,
		    created_at, updated_at)
		 values ($1,$2,$3,$4::uuid,$5,$6,$7,
		    $8,$9,$10,$11,$12,$13,$14,
		    $15,$16,$17,$18,
		    $19::timestamptz,$20,$21,$22::uuid,$23::text[],$24::uuid,
		    now(), now())`,
		k.ID, k.ProviderID, nullText(k.Label), k.SecretEnvelopeID, nullText(k.SecretLast4),
		nullText(k.AuthMethod), status,
		k.Weight, nullInt(k.Priority), nullText(k.Region), nullText(k.Environment),
		nullText(k.Team), nullText(k.Owner), nullText(k.Notes),
		nullInt(k.DailyLimit), nullInt(k.MonthlyLimit), nullInt(k.RPMLimit), nullInt(k.ConcurrencyLimit),
		nullText(k.ExpiresAt), nullText(k.OwnerTenantID), nullText(k.RotationGroup),
		nullText(k.ImportedBatchID), encodePGArray(k.Tags), nullText(k.CreatedBy))
}

func (st *pgStore) getKey(ctx context.Context, id string) (Key, bool, error) {
	var k Key
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select `+keyColumns+` from provider_keys where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		k = scanKey(res.Rows[0])
		found = true
		return nil
	})
	return k, found, err
}

// listKeys is the bounded, cursor-paginated List (keyset on created_at DESC, id DESC).
func (st *pgStore) listKeys(ctx context.Context, f KeyFilter, cur db.Cursor, limit int) ([]Key, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	where, args := buildKeyWhere(f)
	if len(cur.K) == 2 {
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d::timestamptz, $%d::uuid)", len(args)+1, len(args)+2))
		args = append(args, cur.K[0], cur.K[1])
	}
	sql := `select ` + keyColumns + ` from provider_keys`
	if len(where) > 0 {
		sql += " where " + joinAnd(where)
	}
	sql += fmt.Sprintf(" order by created_at desc, id desc limit %d", limit+1)

	var out []Key
	var next db.Cursor
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			last := scanKey(rows[limit-1])
			next = db.Cursor{K: []string{last.CreatedAt, last.ID}}
			rows = rows[:limit]
		}
		for _, r := range rows {
			out = append(out, scanKey(r))
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// buildKeyWhere renders the filter into positional predicates + args.
func buildKeyWhere(f KeyFilter) ([]string, []any) {
	var where []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if f.ProviderID != "" {
		add("provider_id = $%d", f.ProviderID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Health != "" {
		add("health = $%d", f.Health)
	}
	if f.Region != "" {
		add("region = $%d", f.Region)
	}
	if f.Environment != "" {
		add("environment = $%d", f.Environment)
	}
	if f.RotationGroup != "" {
		add("rotation_group = $%d", f.RotationGroup)
	}
	if f.ImportedBatchID != "" {
		add("imported_batch_id = $%d::uuid", f.ImportedBatchID)
	}
	if f.Tag != "" {
		add("tags @> array[$%d]::text[]", f.Tag)
	}
	if f.PoolID != "" {
		add("id in (select key_id from key_pool_members where pool_id = $%d::uuid)", f.PoolID)
	}
	if f.Q != "" {
		add("label ilike $%d", "%"+f.Q+"%")
	}
	return where, args
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " and "
		}
		out += p
	}
	return out
}

// updateKeyMeta applies a metadata patch and returns the refreshed row. Ciphertext is never in
// scope. Returns found=false when no such key.
func (st *pgStore) updateKeyMeta(ctx context.Context, id string, p KeyPatch) (Key, bool, error) {
	sets, args := buildKeyPatch(p)
	if len(sets) == 0 {
		return st.getKey(ctx, id)
	}
	args = append(args, id)
	sql := "update provider_keys set " + joinComma(sets) + ", updated_at = now() where id = $" + strconv.Itoa(len(args))

	var k Key
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(sql, args...); err != nil {
			return err
		}
		res, err := c.QueryParams(`select `+keyColumns+` from provider_keys where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		k = scanKey(res.Rows[0])
		found = true
		return nil
	})
	return k, found, err
}

func buildKeyPatch(p KeyPatch) ([]string, []any) {
	var sets []string
	var args []any
	set := func(col, cast string, val any) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
	}
	if p.Label != nil {
		set("label", "", nullText(*p.Label))
	}
	if p.AuthMethod != nil {
		set("auth_method", "", nullText(*p.AuthMethod))
	}
	if p.Weight != nil {
		set("weight", "", *p.Weight)
	}
	if p.Priority != nil {
		set("priority", "", *p.Priority)
	}
	if p.Region != nil {
		set("region", "", nullText(*p.Region))
	}
	if p.Environment != nil {
		set("environment", "", nullText(*p.Environment))
	}
	if p.Team != nil {
		set("team", "", nullText(*p.Team))
	}
	if p.Owner != nil {
		set("owner", "", nullText(*p.Owner))
	}
	if p.Notes != nil {
		set("notes", "", nullText(*p.Notes))
	}
	if p.DailyLimit != nil {
		set("daily_limit", "", *p.DailyLimit)
	}
	if p.MonthlyLimit != nil {
		set("monthly_limit", "", *p.MonthlyLimit)
	}
	if p.RPMLimit != nil {
		set("rpm_limit", "", *p.RPMLimit)
	}
	if p.ConcurrencyLimit != nil {
		set("concurrency_limit", "", *p.ConcurrencyLimit)
	}
	if p.RotationGroup != nil {
		set("rotation_group", "", nullText(*p.RotationGroup))
	}
	if p.ExpiresAt != nil {
		set("expires_at", "::timestamptz", nullText(*p.ExpiresAt))
	}
	if p.Tags != nil {
		set("tags", "::text[]", encodePGArray(*p.Tags))
	}
	return sets, args
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// setStatus applies a status transition (optionally recording a disable_reason) and returns the
// refreshed row.
// setStatus flips a key's KM-3 status. When inTx is non-nil it runs INSIDE the same transaction
// after the update, with the fresh row — the SEC-7 same-tx audit hook (T5d): the service appends
// the audit row through it via audit.AppendConn, so the state flip and its audit row commit or
// roll back atomically (an audit failure aborts the write, strictly stronger than the logged
// follow-up fallback).
func (st *pgStore) setStatus(ctx context.Context, id, to, reason string, inTx func(*pg.Conn, Key) error) (Key, bool, error) {
	var k Key
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(
			`update provider_keys set status = $2, disable_reason = $3, updated_at = now() where id = $1`,
			id, to, nullText(reason)); err != nil {
			return err
		}
		res, err := c.QueryParams(`select `+keyColumns+` from provider_keys where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		k = scanKey(res.Rows[0])
		found = true
		if inTx != nil {
			return inTx(c, k)
		}
		return nil
	})
	return k, found, err
}

// rotateKey inserts the successor Key and, in the SAME transaction, marks the predecessor
// status='rotating' with the overlap deadline + rotation lineage (rotated_to). overlapUntil is a
// timestamptz text value, or "" for the compromise path (immediate archive of the old key).
// A non-nil inTx hook runs last inside the transaction — the SEC-7 same-tx audit seam (T5d), so
// the successor insert, predecessor flip, and audit row are one atomic unit.
func (st *pgStore) rotateKey(ctx context.Context, oldID string, successor Key, overlapUntil string, inTx func(*pg.Conn) error) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := insertKeyTx(c, successor); err != nil {
			return err
		}
		if overlapUntil == "" {
			// Compromise mode: old key archived immediately, successor already active.
			if err := c.ExecParams(
				`update provider_keys set status = $2, rotated_to = $3::uuid, last_rotated_at = now(),
				   updated_at = now() where id = $1`,
				oldID, StatusArchived, successor.ID); err != nil {
				return err
			}
		} else if err := c.ExecParams(
			`update provider_keys set status = $2, rotated_to = $3::uuid, rotate_overlap_until = $4::timestamptz,
			   last_rotated_at = now(), updated_at = now() where id = $1`,
			oldID, StatusRotating, successor.ID, overlapUntil); err != nil {
			return err
		}
		if inTx != nil {
			return inTx(c)
		}
		return nil
	})
}

// touchKey stamps a timestamp column (last_health_at / credits_synced_at / last_used_at) to now()
// and returns the refreshed row. col is a fixed identifier chosen by the caller (never user input).
func (st *pgStore) touchKey(ctx context.Context, id, col string) (Key, bool, error) {
	var k Key
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		// col is a caller-fixed identifier (never user input); id stays parameterized.
		if err := c.ExecParams(`update provider_keys set `+col+` = now(), updated_at = now() where id = $1`, id); err != nil {
			return err
		}
		res, err := c.QueryParams(`select `+keyColumns+` from provider_keys where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		k = scanKey(res.Rows[0])
		found = true
		return nil
	})
	return k, found, err
}

// fingerprintDup reports the id of an existing provider_keys row (same provider) whose sealed
// material shares the KEYED HMAC fingerprint of the just-sealed envelope — the duplicate check
// runs entirely in SQL against secret_envelopes.aad_fingerprint and NEVER decrypts anything.
func (st *pgStore) fingerprintDup(ctx context.Context, providerID, envelopeID string) (string, bool, error) {
	var dup string
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(
			`select pk.id from provider_keys pk
			   join secret_envelopes se on se.id = pk.secret_envelope_id
			  where pk.provider_id = $1
			    and se.aad_fingerprint is not null
			    and se.aad_fingerprint = (select aad_fingerprint from secret_envelopes where id = $2)
			  limit 1`,
			providerID, envelopeID)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			dup = s(res.Rows[0][0])
			found = true
		}
		return nil
	})
	return dup, found, err
}

// fingerprintDupDetail is fingerprintDup plus the duplicate key's imported_batch_id. A bulk-job
// RESUME re-processes rows past the last committed cursor (OI-KEYS-1c); a row that this SAME import
// batch already committed reappears as a fingerprint duplicate whose imported_batch_id equals the
// running batch — sameBatch=true. The executor treats that as an idempotent skip (already imported,
// not a conflict and not a second insert), so a re-attempt neither double-inserts nor mis-counts a
// committed row as failed. A dup owned by a DIFFERENT batch/key is a genuine conflict.
func (st *pgStore) fingerprintDupDetail(ctx context.Context, providerID, envelopeID, batchID string) (dupID string, sameBatch, found bool, err error) {
	err = st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, e := c.QueryParams(
			`select pk.id, coalesce(pk.imported_batch_id::text,'') from provider_keys pk
			   join secret_envelopes se on se.id = pk.secret_envelope_id
			  where pk.provider_id = $1
			    and se.aad_fingerprint is not null
			    and se.aad_fingerprint = (select aad_fingerprint from secret_envelopes where id = $2)
			  limit 1`,
			providerID, envelopeID)
		if e != nil {
			return e
		}
		if len(res.Rows) > 0 {
			dupID = s(res.Rows[0][0])
			sameBatch = batchID != "" && s(res.Rows[0][1]) == batchID
			found = true
		}
		return nil
	})
	return dupID, sameBatch, found, err
}

// fingerprintPrefix returns the first 8 hex chars of an envelope's keyed fingerprint (the
// display "fingerprint_prefix", doc 04 §2.4) without touching ciphertext, or "" when absent.
func (st *pgStore) fingerprintPrefix(ctx context.Context, envelopeID string) (string, error) {
	out := ""
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select encode(aad_fingerprint, 'hex') from secret_envelopes where id = $1`, envelopeID)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			h := s(res.Rows[0][0])
			if len(h) >= 8 {
				out = h[:8]
			} else {
				out = h
			}
		}
		return nil
	})
	return out, err
}

// deleteEnvelope removes a just-created envelope that turned out to duplicate an existing key, so
// a rejected import row leaves no orphan ciphertext behind.
func (st *pgStore) deleteEnvelope(ctx context.Context, envelopeID string) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`delete from secret_envelopes where id = $1`, envelopeID)
	})
}

// --- key_import_batches ---

func (st *pgStore) insertBatch(ctx context.Context, b ImportBatch) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into key_import_batches (id, provider_id, source, total, status, created_by)
			 values ($1,$2,$3,$4,$5,$6::uuid)`,
			b.ID, b.ProviderID, b.Source, b.Total, b.Status, nullText(b.CreatedBy))
	})
}

func (st *pgStore) getBatch(ctx context.Context, id string) (ImportBatch, bool, error) {
	var b ImportBatch
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select `+batchColumns+` from key_import_batches where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		b = scanBatch(res.Rows[0])
		found = true
		return nil
	})
	return b, found, err
}

func (st *pgStore) setBatchTotal(ctx context.Context, id string, total int) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update key_import_batches set total = $2 where id = $1`, id, total)
	})
}

func (st *pgStore) updateBatchProgress(ctx context.Context, id string, succeeded, failed int) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update key_import_batches set succeeded = $2, failed = $3 where id = $1`, id, succeeded, failed)
	})
}

func (st *pgStore) finishBatch(ctx context.Context, id, status, errorsJSON string) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`update key_import_batches set status = $2, errors = $3::jsonb, finished_at = now() where id = $1`,
			id, status, nullText(errorsJSON))
	})
}

// --- key_pools + key_pool_members ---

func (st *pgStore) insertPool(ctx context.Context, p Pool) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(
			`insert into key_pools (id, provider_id, name, strategy, strategy_params, owner_tenant_id, status)
			 values ($1,$2,$3,$4,$5::jsonb,$6,$7)`,
			p.ID, p.ProviderID, p.Name, p.Strategy, nullText(p.StrategyParams),
			nullText(p.OwnerTenantID), poolStatusOrDefault(p.Status))
	})
}

func poolStatusOrDefault(s string) string {
	if s == "" {
		return "active"
	}
	return s
}

func (st *pgStore) getPool(ctx context.Context, id string) (Pool, bool, error) {
	var p Pool
	found := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select `+poolColumns+` from key_pools where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		p = scanPool(res.Rows[0])
		cnt, err := c.QueryParams(`select count(*) from key_pool_members where pool_id = $1`, id)
		if err != nil {
			return err
		}
		p.MemberCount = int(i64(cnt.Rows[0][0]))
		found = true
		return nil
	})
	return p, found, err
}

func (st *pgStore) listPools(ctx context.Context, providerID, strategy, ownerTenant string, cur db.Cursor, limit int) ([]Pool, db.Cursor, error) {
	limit = db.ClampLimit(limit)
	var where []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if providerID != "" {
		add("provider_id = $%d", providerID)
	}
	if strategy != "" {
		add("strategy = $%d", strategy)
	}
	if ownerTenant != "" {
		add("owner_tenant_id = $%d", ownerTenant)
	}
	if cur.ID != "" {
		add("id > $%d::uuid", cur.ID)
	}
	sql := `select ` + poolColumns + ` from key_pools`
	if len(where) > 0 {
		sql += " where " + joinAnd(where)
	}
	sql += fmt.Sprintf(" order by id asc limit %d", limit+1)

	var out []Pool
	var next db.Cursor
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{ID: s(rows[limit-1][0])}
			rows = rows[:limit]
		}
		for _, r := range rows {
			out = append(out, scanPool(r))
		}
		return nil
	})
	if err != nil {
		return nil, db.Cursor{}, err
	}
	return out, next, nil
}

// updatePool applies name/status/strategy_params changes (PATCH /key-pools/{id}).
func (st *pgStore) updatePool(ctx context.Context, id string, name, status, strategyParams *string) (bool, error) {
	var sets []string
	var args []any
	set := func(col, cast string, val any) {
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
	}
	if name != nil {
		set("name", "", *name)
	}
	if status != nil {
		set("status", "", *status)
	}
	if strategyParams != nil {
		set("strategy_params", "::jsonb", nullText(*strategyParams))
	}
	if len(sets) == 0 {
		return true, nil
	}
	args = append(args, id)
	sql := "update key_pools set " + joinComma(sets) + " where id = $" + strconv.Itoa(len(args))
	affected := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(sql, args...); err != nil {
			return err
		}
		res, err := c.QueryParams(`select 1 from key_pools where id = $1`, id)
		if err != nil {
			return err
		}
		affected = len(res.Rows) > 0
		return nil
	})
	return affected, err
}

func (st *pgStore) setPoolStrategy(ctx context.Context, id, strategy, params string) (bool, error) {
	affected := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		if err := c.ExecParams(
			`update key_pools set strategy = $2, strategy_params = $3::jsonb where id = $1`,
			id, strategy, nullText(params)); err != nil {
			return err
		}
		res, err := c.QueryParams(`select 1 from key_pools where id = $1`, id)
		if err != nil {
			return err
		}
		affected = len(res.Rows) > 0
		return nil
	})
	return affected, err
}

func (st *pgStore) deletePool(ctx context.Context, id string) (bool, error) {
	affected := false
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select 1 from key_pools where id = $1`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return nil
		}
		affected = true
		if err := c.ExecParams(`delete from key_pool_members where pool_id = $1`, id); err != nil {
			return err
		}
		return c.ExecParams(`delete from key_pools where id = $1`, id)
	})
	return affected, err
}

// replaceMembers performs a full-replacement PUT of a pool's member key set inside one
// transaction (delete-all + re-insert). Missing key ids are reported so the caller can 422.
func (st *pgStore) replaceMembers(ctx context.Context, poolID string, keyIDs []string) (missing []string, ok bool, err error) {
	err = st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, e := c.QueryParams(`select 1 from key_pools where id = $1`, poolID)
		if e != nil {
			return e
		}
		if len(res.Rows) == 0 {
			return nil
		}
		ok = true
		for _, kid := range keyIDs {
			r, e := c.QueryParams(`select 1 from provider_keys where id = $1`, kid)
			if e != nil {
				return e
			}
			if len(r.Rows) == 0 {
				missing = append(missing, kid)
			}
		}
		if len(missing) > 0 {
			return nil // validated before any mutation
		}
		if e := c.ExecParams(`delete from key_pool_members where pool_id = $1`, poolID); e != nil {
			return e
		}
		for _, kid := range keyIDs {
			if e := c.ExecParams(
				`insert into key_pool_members (pool_id, key_id) values ($1,$2::uuid) on conflict do nothing`,
				poolID, kid); e != nil {
				return e
			}
		}
		return nil
	})
	return missing, ok, err
}

// addKeyToPools inserts a key into each of the given pools (create-time pool_ids); duplicates and
// unknown pools are ignored so a membership hiccup never fails a key create.
func (st *pgStore) addKeyToPools(ctx context.Context, keyID string, poolIDs []string) error {
	return st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		for _, pid := range poolIDs {
			r, err := c.QueryParams(`select 1 from key_pools where id = $1`, pid)
			if err != nil {
				return err
			}
			if len(r.Rows) == 0 {
				continue
			}
			if err := c.ExecParams(
				`insert into key_pool_members (pool_id, key_id) values ($1::uuid, $2::uuid) on conflict do nothing`,
				pid, keyID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (st *pgStore) poolMemberIDs(ctx context.Context, poolID string) ([]string, error) {
	var ids []string
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(`select key_id from key_pool_members where pool_id = $1 order by key_id`, poolID)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			ids = append(ids, s(r[0]))
		}
		return nil
	})
	return ids, err
}

// listKeyIDsByFilter resolves a bulk-op filter to the set of matching key ids under the platform
// RLS context (re-evaluated at execution time, doc 04 §4.2).
func (st *pgStore) listKeyIDsByFilter(ctx context.Context, f KeyFilter) ([]string, error) {
	where, args := buildKeyWhere(f)
	sql := `select id from provider_keys`
	if len(where) > 0 {
		sql += " where " + joinAnd(where)
	}
	sql += " order by id"
	var ids []string
	err := st.db.PlatformTx(ctx, func(c *pg.Conn) error {
		res, err := c.QueryParams(sql, args...)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			ids = append(ids, s(r[0]))
		}
		return nil
	})
	return ids, err
}
