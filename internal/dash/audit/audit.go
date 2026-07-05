// Package audit is the per-Tenant SHA-256 hash-chained evidence log (doc 05 §8, ADR-0018).
// Every privileged action appends one row whose hash = sha256(prev_hash || canonical_json);
// appends are serialized per Tenant by a FOR UPDATE lock on the audit_chain_heads row, in the
// SAME transaction as the business write, so a write and its audit row commit or roll back
// together.
//
// Gates enforced:
//   - G1 tenant isolation. tenant_id is read from the ctx Principal (via db.Store.Tx), NEVER
//     from arguments; audit_log is Class T under FORCE RLS.
//   - Tamper-evidence / non-repudiation. The chain links each row to its predecessor; Verify
//     recomputes the chain and reports the first break.
//   - Bounded queries. List is keyset-paginated (seq DESC) under db.ClampLimit.
package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Entry is the caller-supplied part of an audit row. tenant_id, seq, prev_hash, hash, and
// created_at are supplied by Append — never by the caller. Before/After are redacted snapshots
// (envelope ids + last4, never secrets, per doc 05 §7.3).
type Entry struct {
	Action      string          // required; the audited verb (e.g. "provider_pause", "login")
	ObjectKind  string          // the target table/route, or ""
	ObjectID    string          // the target id, or ""
	ActorUserID string          // the acting User id, or "" for pre-auth/system events
	ActorRole   string          // operator | tenant_admin | tenant_user | "" (system)
	IP          string          // client ip, or ""
	Before      json.RawMessage // pre-image snapshot, or nil
	After       json.RawMessage // post-image snapshot, or nil
}

// Log appends to and verifies the per-Tenant chain.
type Log struct {
	store *db.Store
	now   func() time.Time // injectable clock; defaults to time.Now
}

// New builds a Log over store with the wall clock.
func New(store *db.Store) *Log { return &Log{store: store, now: time.Now} }

// Append writes one chained audit row inside a dual-GUC transaction. It locks the Tenant's
// audit_chain_heads row (initializing genesis prev_hash = 32 zero bytes at seq 1 when absent),
// computes the next hash, INSERTs the audit_log row, and UPSERTs the chain head — all under
// the caller's Principal tenant. tenant_id comes from ctx, never from e.
func (l *Log) Append(ctx context.Context, e Entry) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err // fail-closed
	}
	return l.store.Tx(ctx, func(c *pg.Conn) error {
		return l.appendOn(c, p.TenantID, e)
	})
}

// AppendConn writes one chained audit row on an ALREADY-OPEN dual-GUC connection, so a
// business write and its audit row commit or roll back together (doc 05 §8.1's
// same-transaction guarantee — the resolution of Deviation D-P0-3 for callers that thread a
// *pg.Conn, e.g. configver's publish tx). The caller owns the transaction; tenant_id comes
// from ctx, never from e, and MUST equal the tenant bound on c.
func (l *Log) AppendConn(ctx context.Context, c *pg.Conn, e Entry) error {
	p, err := tenant.FromContext(ctx)
	if err != nil {
		return err // fail-closed
	}
	return l.appendOn(c, p.TenantID, e)
}

// appendOn performs the chain-head lock, hash computation, row insert, and head upsert on c.
func (l *Log) appendOn(c *pg.Conn, tenantID string, e Entry) error {
	created := l.now().UTC().Truncate(time.Microsecond)
	res, err := c.QueryParams(
		`select last_seq, last_hash from audit_chain_heads where tenant_id = $1 for update`,
		tenantID)
	if err != nil {
		return err
	}
	var lastSeq int64
	prevHash := make([]byte, 32) // genesis
	haveHead := false
	if len(res.Rows) > 0 {
		haveHead = true
		row := res.Rows[0]
		if row[0] != nil {
			lastSeq, _ = strconv.ParseInt(*row[0], 10, 64)
		}
		if row[1] != nil {
			if prevHash, err = decodeBytea(*row[1]); err != nil {
				return err
			}
		}
	}
	seq := lastSeq + 1

	canon, err := canonicalize(record{TenantID: tenantID, Seq: seq, CreatedAt: created, Entry: e})
	if err != nil {
		return err
	}
	hash := computeHash(prevHash, canon)

	if err := c.ExecParams(
		`insert into audit_log
		   (tenant_id, seq, actor_user_id, actor_role, action, object_kind, object_id,
		    before, after, ip, prev_hash, hash, created_at)
		 values ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11::bytea,$12::bytea,$13)`,
		tenantID, seq,
		nullIf(e.ActorUserID), nullIf(e.ActorRole), e.Action,
		nullIf(e.ObjectKind), nullIf(e.ObjectID),
		jsonbOrNull(e.Before), jsonbOrNull(e.After), nullIf(e.IP),
		encodeBytea(prevHash), encodeBytea(hash), created,
	); err != nil {
		return err
	}

	if haveHead {
		return c.ExecParams(
			`update audit_chain_heads set last_seq = $2, last_hash = $3::bytea where tenant_id = $1`,
			tenantID, seq, encodeBytea(hash))
	}
	return c.ExecParams(
		`insert into audit_chain_heads (tenant_id, last_seq, last_hash) values ($1,$2,$3::bytea)`,
		tenantID, seq, encodeBytea(hash))
}

// Verify walks tenantID's chain in seq order, recomputing every hash, and returns ok=false
// with the seq of the first row whose stored prev_hash/hash disagrees with the recomputation.
// The transaction is bound to the ctx Principal; an operator may verify any Tenant (audit_log
// has the enumerated operator SELECT policy), a tenant_admin only its own.
func (l *Log) Verify(ctx context.Context, tenantID string) (ok bool, brokenSeq int64, err error) {
	ok = true
	txErr := l.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select seq, actor_user_id, actor_role, action, object_kind, object_id,
			        before, after, ip, prev_hash, hash, created_at
			 from audit_log where tenant_id = $1 order by seq asc`, tenantID)
		if qerr != nil {
			return qerr
		}
		prevHash := make([]byte, 32)
		for _, row := range res.Rows {
			seq, _ := strconv.ParseInt(str(row[0]), 10, 64)
			e := Entry{
				ActorUserID: str(row[1]),
				ActorRole:   str(row[2]),
				Action:      str(row[3]),
				ObjectKind:  str(row[4]),
				ObjectID:    str(row[5]),
				Before:      rawFromPtr(row[6]),
				After:       rawFromPtr(row[7]),
				IP:          str(row[8]),
			}
			storedPrev, derr := decodeByteaPtr(row[9])
			if derr != nil {
				return derr
			}
			storedHash, derr := decodeByteaPtr(row[10])
			if derr != nil {
				return derr
			}
			created := parseTS(str(row[11]))
			canon, cerr := canonicalize(record{TenantID: tenantID, Seq: seq, CreatedAt: created, Entry: e})
			if cerr != nil {
				return cerr
			}
			want := computeHash(prevHash, canon)
			if !bytes.Equal(storedPrev, prevHash) || !bytes.Equal(storedHash, want) {
				ok = false
				brokenSeq = seq
				return nil // stop at first break
			}
			prevHash = storedHash
		}
		return nil
	})
	if txErr != nil {
		return false, 0, txErr
	}
	return ok, brokenSeq, nil
}

// List returns audit entries newest-first (keyset on seq DESC), bounded by db.ClampLimit. The
// returned cursor is non-empty when more rows remain (its K[0] is the last seq served). Rows
// are RLS-scoped to the caller's Principal.
func (l *Log) List(ctx context.Context, cur db.Cursor, limit int) (entries []Entry, next db.Cursor, err error) {
	limit = db.ClampLimit(limit)
	txErr := l.store.Tx(ctx, func(c *pg.Conn) error {
		const cols = `seq, actor_user_id, actor_role, action, object_kind, object_id, before, after, ip`
		var res *pg.Result
		var qerr error
		if len(cur.K) > 0 && cur.K[0] != "" {
			after, perr := strconv.ParseInt(cur.K[0], 10, 64)
			if perr != nil {
				return db.ErrInvalidCursor
			}
			res, qerr = c.QueryParams(
				`select `+cols+` from audit_log where seq < $1 order by seq desc limit $2`,
				after, int64(limit+1))
		} else {
			res, qerr = c.QueryParams(
				`select `+cols+` from audit_log order by seq desc limit $1`,
				int64(limit+1))
		}
		if qerr != nil {
			return qerr
		}
		rows := res.Rows
		if len(rows) > limit {
			next = db.Cursor{K: []string{str(rows[limit-1][0])}}
			rows = rows[:limit]
		}
		for _, row := range rows {
			entries = append(entries, Entry{
				ActorUserID: str(row[1]),
				ActorRole:   str(row[2]),
				Action:      str(row[3]),
				ObjectKind:  str(row[4]),
				ObjectID:    str(row[5]),
				Before:      rawFromPtr(row[6]),
				After:       rawFromPtr(row[7]),
				IP:          str(row[8]),
			})
		}
		return nil
	})
	if txErr != nil {
		return nil, db.Cursor{}, txErr
	}
	return entries, next, nil
}

// --- small column helpers (kept local so audit stays self-contained) ---

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func jsonbOrNull(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func rawFromPtr(p *string) json.RawMessage {
	if p == nil {
		return nil
	}
	return json.RawMessage(*p)
}

// encodeBytea / decodeBytea round-trip a bytea column through \x hex text (the internal/pg
// client sends params as text and has no []byte encoder).
func encodeBytea(b []byte) string { return `\x` + hex.EncodeToString(b) }

func decodeBytea(s string) ([]byte, error) {
	if len(s) >= 2 && s[0] == '\\' && (s[1] == 'x' || s[1] == 'X') {
		return hex.DecodeString(s[2:])
	}
	return nil, fmt.Errorf("audit: bytea not in \\x hex form")
}

func decodeByteaPtr(p *string) ([]byte, error) {
	if p == nil {
		return make([]byte, 32), nil // prev_hash/hash are NOT NULL; defensive
	}
	return decodeBytea(*p)
}

// parseTS parses a Postgres timestamptz text rendering (or RFC3339) into a time.Time.
func parseTS(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
