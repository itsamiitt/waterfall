package keys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// Durable bulk-job lifecycle for module 3 (OI-KEYS-1). Both the key bulk-op (POST /keys/bulk,
// kind=key_bulk) and the async import (POST /providers/{id}/keys/import, kind=key_import) record
// their JOB lifecycle on the durable bulk_jobs table (migration 0008) using the same lease/claim
// model the queues replayer uses: a queued row is claimed under this instance's lease, the lease
// is renewed while processing, per-row results/errors are persisted, and a terminal status is
// committed. GET /bulk-jobs/{id} (the queues package's durable reader) then reports progress for
// these kinds too. key_import_batches is kept as the per-row provenance detail (imported_batch_id
// still references it); the batch id and the bulk_jobs id are the SAME uuid so both the durable
// reader and GET /key-imports/{job_id} resolve one job.
//
// RLS: bulk_jobs is Class T (tenant-scoped, migration 0008) — every write runs through the
// tenant-scoped db.Store.Tx (NOT PlatformTx), so the row's tenant_id binds to the caller's
// Principal via current_setting('app.current_tenant'). Platform operators (the common keys path)
// write tenant_id='platform', which is exactly what the platform-scoped janitor sweeps.

// bulk_jobs.kind values written by this package (doc 04 §4).
const (
	bulkKindKeyBulk   = "key_bulk"
	bulkKindKeyImport = "key_import"
)

// keysBulkLease is this executor's liveness lease on its bulk_jobs row (doc 04 §4.1). It is renewed
// on each progress commit; on expiry the queues janitor re-claims or terminally-fails the row.
const keysBulkLease = 60 * time.Second

// bulkResultsCap bounds the per-item results/errors persisted on a bulk_jobs row (counts stay
// exact; doc 06 §3.4). It mirrors the import error cap.
const bulkResultsCap = maxImportErrors

// ErrBulkInFlight reports that a bulk job for the same (tenant, kind, scope) is already in flight —
// the bulk_jobs one-in-flight partial unique index (409 bulk_job_conflict, doc 04 §4.2).
var ErrBulkInFlight = errors.New("keys: a bulk job is already in flight for this scope")

// bulkResultItem is one row's bulk-op outcome persisted in bulk_jobs.results.
type bulkResultItem struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"` // "ok" | "error"
}

func keysBulkLeaseInterval() string {
	return fmt.Sprintf("%d seconds", int(keysBulkLease.Seconds()))
}

// insertBulkJob inserts the queued bulk_jobs row (tenant-scoped RLS). id is the caller-chosen job
// id (for imports it equals the key_import_batches id). A duplicate (tenant, kind, scope) in flight
// trips the partial unique index and is reported as ErrBulkInFlight.
func (st *pgStore) insertBulkJob(ctx context.Context, id, kind, scope string, total, matched int, by string) error {
	err := st.db.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`insert into bulk_jobs
			(id, tenant_id, kind, scope_fingerprint, status, total, matched_at_execution, created_by)
			values ($1::uuid, current_setting('app.current_tenant'), $2, $3, 'queued', $4, $5, $6::uuid)`,
			id, kind, scope, int64(total), int64(matched), uuidArg(by))
	})
	if isUniqueViolation(err) {
		return ErrBulkInFlight
	}
	return err
}

// claimBulkJob transitions the queued row to running under this instance's lease. ok=false when
// another instance already claimed it (or the row is gone).
func (st *pgStore) claimBulkJob(ctx context.Context, id, instanceID string) (bool, error) {
	ok := false
	err := st.db.Tx(ctx, func(c *pg.Conn) error {
		res, e := c.QueryParams(`update bulk_jobs set status='running', claimed_by=$2,
			lease_expires_at=now() + interval '`+keysBulkLeaseInterval()+`', started_at=now(), attempts=attempts+1
		  where id=$1::uuid and status='queued' returning id`, id, instanceID)
		if e != nil {
			return e
		}
		ok = len(res.Rows) > 0 && res.Rows[0][0] != nil
		return nil
	})
	return ok, err
}

// renewBulkJob flushes intermediate counters and renews the lease (so a live executor is not
// reclaimed by the janitor mid-run).
func (st *pgStore) renewBulkJob(ctx context.Context, id string, succeeded, failed int) error {
	return st.db.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set succeeded=$2, failed=$3,
			lease_expires_at=now() + interval '`+keysBulkLeaseInterval()+`' where id=$1::uuid`,
			id, int64(succeeded), int64(failed))
	})
}

// finishBulkJob commits the terminal status, final counters, per-item results, and the redacted
// errors/summary, clearing the lease so the one-in-flight index is released.
func (st *pgStore) finishBulkJob(ctx context.Context, id, status string, succeeded, failed int, results, errs, summary string) error {
	return st.db.Tx(ctx, func(c *pg.Conn) error {
		return c.ExecParams(`update bulk_jobs set status=$2, succeeded=$3, failed=$4,
			results=$5::jsonb, errors=$6::jsonb, error_summary=$7::jsonb,
			finished_at=now(), lease_expires_at=null where id=$1::uuid`,
			id, status, int64(succeeded), int64(failed),
			nullText(results), nullText(errs), nullText(summary))
	})
}

// bulkScopeFingerprint is the canonical hash of a bulk-op's op + resolved target id set (order
// independent). It keys the one-in-flight guard so an identical resubmit conflicts (409), while a
// different scope proceeds.
func bulkScopeFingerprint(op string, ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	h := sha256.New()
	h.Write([]byte(op))
	for _, id := range sorted {
		h.Write([]byte{0})
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// isUniqueViolation reports a Postgres 23505 unique-violation (the one-in-flight index).
func isUniqueViolation(err error) bool {
	var pe *pg.PGError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// uuidArg renders a uuid-shaped actor id as a bound arg, or nil so created_by stays NULL (the audit
// chain records the actor regardless; created_by is a convenience column typed uuid).
func uuidArg(s string) any {
	if looksLikeUUID(s) {
		return s
	}
	return nil
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}

// marshalJSONString marshals v to a compact JSON string, or "" when v is nil/empty/unmarshalable.
func marshalJSONString(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
