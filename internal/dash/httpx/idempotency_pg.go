package httpx

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/tenant"
)

// errIdemBytea reports a body_hash column that is not in Postgres \x hex text form.
var errIdemBytea = errors.New("httpx: idempotency body_hash not in \\x hex form")

// pgIdemStore is the durable admin idempotency ledger over dash_admin_idempotency (doc 04 §1.3 /
// OI-API-8, replacing the P0 in-process map, Deviation D-P0-2). It is first-writer-wins on the
// (tenant_id, idempotency_key) primary key: Claim races an INSERT ... ON CONFLICT DO NOTHING, so
// exactly one concurrent caller becomes the writer. Every access is a tenant-scoped db.Store.Tx
// (G1): tenant_id derives from the request Principal and satisfies the row's tenant-isolation
// WITH CHECK.
type pgIdemStore struct{ store *db.Store }

func newPGIdemStore(store *db.Store) *pgIdemStore { return &pgIdemStore{store: store} }

// Claim inserts the placeholder row (status/response NULL) and reports whether this caller won the
// race. RETURNING distinguishes an inserted row (claimed) from an ON CONFLICT no-op (contender).
func (s *pgIdemStore) Claim(ctx context.Context, tenantID, key string, bodyHash []byte) (bool, error) {
	claimed := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`insert into dash_admin_idempotency (tenant_id, idempotency_key, body_hash)
			 values ($1, $2, $3::bytea)
			 on conflict (tenant_id, idempotency_key) do nothing
			 returning tenant_id`,
			tenantID, key, idemEncodeBytea(bodyHash))
		if qerr != nil {
			return qerr
		}
		claimed = len(res.Rows) > 0
		return nil
	})
	return claimed, err
}

// Lookup reads the current record for (tenant, key). A non-NULL status marks it done (the first
// writer recorded its terminal response).
func (s *pgIdemStore) Lookup(ctx context.Context, tenantID, key string) (idemRecord, bool, error) {
	var rec idemRecord
	found := false
	err := s.store.Tx(ctx, func(c *pg.Conn) error {
		res, qerr := c.QueryParams(
			`select body_hash, status, response from dash_admin_idempotency
			 where tenant_id = $1 and idempotency_key = $2`, tenantID, key)
		if qerr != nil {
			return qerr
		}
		if len(res.Rows) == 0 {
			return nil
		}
		row := res.Rows[0]
		bh, derr := idemDecodeBytea(idemStr(row[0]))
		if derr != nil {
			return derr
		}
		rec.bodyHash = bh
		if row[1] != nil { // status non-NULL => completed
			rec.done = true
			rec.status, _ = strconv.Atoi(*row[1])
		}
		if row[2] != nil {
			rec.response = []byte(*row[2])
		}
		found = true
		return nil
	})
	return rec, found, err
}

// Finish records the first writer's terminal status and response body. An empty body stores NULL;
// a non-empty body is stored as jsonb (every admin response is JSON). On replay Lookup returns the
// jsonb text rendering, which is semantically identical to the original body.
func (s *pgIdemStore) Finish(ctx context.Context, tenantID, key string, status int, response []byte) error {
	return s.store.Tx(ctx, func(c *pg.Conn) error {
		var resp any
		if len(bytes.TrimSpace(response)) > 0 {
			resp = string(response)
		}
		return c.ExecParams(
			`update dash_admin_idempotency set status = $3, response = $4::jsonb
			 where tenant_id = $1 and idempotency_key = $2`,
			tenantID, key, status, resp)
	})
}

// DeleteBefore reaps rows created before cutoff (doc 04 §1.3: 24h retention). It enumerates tenants
// under the operator SELECT policy and deletes per-tenant under each tenant's own binding — no
// BYPASSRLS — mirroring Sessions.DeleteExpired.
func (s *pgIdemStore) DeleteBefore(ctx context.Context, cutoff time.Time) error {
	sysCtx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: "platform", Scopes: []string{"role:operator"}})
	var tenants []string
	if err := s.store.Tx(sysCtx, func(c *pg.Conn) error {
		res, err := c.Query(`select id from tenants`)
		if err != nil {
			return err
		}
		for _, row := range res.Rows {
			tenants = append(tenants, idemStr(row[0]))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, tid := range tenants {
		tctx := tenant.WithPrincipal(ctx, tenant.Principal{TenantID: tid})
		_ = s.store.Tx(tctx, func(c *pg.Conn) error {
			return c.ExecParams(`delete from dash_admin_idempotency where created_at < $1`, cutoff)
		})
	}
	return nil
}

var _ idemStore = (*pgIdemStore)(nil)

// idemEncodeBytea / idemDecodeBytea round-trip a bytea column through \x hex text (the internal/pg
// client sends parameters as text and has no []byte encoder), matching internal/dash/secrets and
// internal/dash/audit.
func idemEncodeBytea(b []byte) string { return `\x` + hex.EncodeToString(b) }

func idemDecodeBytea(s string) ([]byte, error) {
	if len(s) >= 2 && s[0] == '\\' && (s[1] == 'x' || s[1] == 'X') {
		return hex.DecodeString(s[2:])
	}
	return nil, errIdemBytea
}

// idemStr dereferences a nullable text column to "" on NULL.
func idemStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
