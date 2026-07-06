package keys

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The bulk-import pipeline (doc 04 §2.4 + §4). A submission creates a key_import_batches row and
// returns 202 {job_id=batch_id}; processing runs ASYNC in a background goroutine (P1's in-process
// execution model, tracked entirely by key_import_batches — see OI note in service.go). Each row
// is validated, its material sealed via secrets.Backend.Seal, checked for duplicates by keyed
// fingerprint, and inserted as a provider_keys row stamped with imported_batch_id.

// canonicalAliases maps recognized header / JSON-key aliases to the canonical import field.
var canonicalAliases = map[string]string{
	"label": "label", "name": "label",
	"secret": "secret", "key": "secret", "api_key": "secret", "apikey": "secret", "value": "secret",
	"region":      "region",
	"environment": "environment", "env": "environment",
	"pool": "pool", "pool_name": "pool",
	"weight":      "weight",
	"priority":    "priority",
	"daily_limit": "daily_limit", "daily": "daily_limit",
	"monthly_limit": "monthly_limit", "monthly": "monthly_limit",
	"rpm_limit": "rpm_limit", "rpm": "rpm_limit",
}

// parseRows dispatches on source format and returns parsed rows. It enforces the row cap.
func parseRows(source string, data []byte) ([]importRow, error) {
	var records [][]string
	var err error
	switch source {
	case "json":
		return parseJSONRows(data)
	case "xlsx":
		records, err = readXLSX(data)
	case "csv", "paste":
		records, err = parseDelimited(data)
	default:
		return nil, fmt.Errorf("keys: unsupported import format %q", source)
	}
	if err != nil {
		return nil, err
	}
	return rowsFromRecords(records)
}

// parseDelimited reads CSV/paste text into raw records via encoding/csv (lenient field counts).
func parseDelimited(data []byte) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // ragged rows are tolerated; missing columns read as empty
	r.TrimLeadingSpace = true
	var records [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errBadCSV
		}
		records = append(records, rec)
		if len(records) > maxImportRows+1 { // +1 for the header
			return nil, errTooManyRows
		}
	}
	return records, nil
}

// rowsFromRecords maps a header + data records into importRows using the alias table.
func rowsFromRecords(records [][]string) ([]importRow, error) {
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	idx := map[string]int{}
	for i, h := range header {
		if canon, ok := canonicalAliases[strings.ToLower(strings.TrimSpace(h))]; ok {
			if _, seen := idx[canon]; !seen {
				idx[canon] = i
			}
		}
	}
	get := func(rec []string, field string) string {
		if i, ok := idx[field]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	data := records[1:]
	if len(data) > maxImportRows {
		return nil, errTooManyRows
	}
	out := make([]importRow, 0, len(data))
	for _, rec := range data {
		out = append(out, importRow{
			Label:        get(rec, "label"),
			Secret:       get(rec, "secret"),
			Region:       get(rec, "region"),
			Environment:  get(rec, "environment"),
			Pool:         get(rec, "pool"),
			Weight:       parseIntPtr(get(rec, "weight")),
			Priority:     parseIntPtr(get(rec, "priority")),
			DailyLimit:   parseIntPtr(get(rec, "daily_limit")),
			MonthlyLimit: parseIntPtr(get(rec, "monthly_limit")),
			RPMLimit:     parseIntPtr(get(rec, "rpm_limit")),
		})
	}
	return out, nil
}

// parseJSONRows reads a JSON array of objects (canonical or aliased keys).
func parseJSONRows(data []byte) ([]importRow, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw []map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, errBadJSON
	}
	if len(raw) > maxImportRows {
		return nil, errTooManyRows
	}
	out := make([]importRow, 0, len(raw))
	for _, obj := range raw {
		m := map[string]string{}
		for k, v := range obj {
			if canon, ok := canonicalAliases[strings.ToLower(strings.TrimSpace(k))]; ok {
				m[canon] = anyToString(v)
			}
		}
		out = append(out, importRow{
			Label:        m["label"],
			Secret:       m["secret"],
			Region:       m["region"],
			Environment:  m["environment"],
			Pool:         m["pool"],
			Weight:       parseIntPtr(m["weight"]),
			Priority:     parseIntPtr(m["priority"]),
			DailyLimit:   parseIntPtr(m["daily_limit"]),
			MonthlyLimit: parseIntPtr(m["monthly_limit"]),
			RPMLimit:     parseIntPtr(m["rpm_limit"]),
		})
	}
	return out, nil
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func parseIntPtr(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// bulkProgressEvery bounds how often the import executor flushes counters, renews its lease, and
// polls cancel_requested against the durable bulk_jobs row. Bounding these to a wave (not per-row)
// keeps a 50k import from issuing a control write per row while still surfacing a cancellation or a
// lost lease within one wave (doc 04 §4.1, doc 15 §T3/§T5b).
const bulkProgressEvery = 50

// driveClaimedImport (re-)drives an ALREADY-CLAIMED key_import bulk_jobs row (claimed_by=instanceID,
// status='running') to a terminal state, resuming past the last committed cursor. It is the single
// execution path shared by the initial in-process executor (StartImport) and the crash-recovery
// BulkJobRunner (OI-KEYS-1c). The parsed rows live ONLY in this instance's in-memory staging
// registry — plaintext key material is never persisted (doc 05 §7.3), so a survivor that lacks the
// staged payload parks the job 'failed' for operator resubmit rather than guessing.
func (svc *Service) driveClaimedImport(ctx context.Context, jobID, instanceID string, startSucceeded, startFailed int) {
	stg, ok := svc.stage.get(jobID)
	if !ok {
		owned, _ := svc.store.finishBulkJobOwned(ctx, jobID, instanceID, StatusImportFailed, startSucceeded, startFailed,
			"", "", marshalJSONString(map[string]any{"reason": "resume payload unavailable on this instance; resubmit"}))
		if owned {
			_ = svc.store.finishBatch(ctx, jobID, StatusImportFailed, "")
			svc.log.Warn("import resume parked: staged payload unavailable", "job", jobID)
		}
		return
	}
	svc.runImportRows(ctx, jobID, instanceID, stg, startSucceeded, startFailed)
}

// runImportRows is the per-row seal→dedupe→insert loop. ctx carries the captured Principal; all
// key persistence is Class P via PlatformTx while the bulk_jobs lease is tenant-scoped. Rows commit
// independently, so `succeeded`+`failed` is a durable resume cursor: a re-attempt starts past it,
// and a row this SAME batch already committed is recognized as a same-batch fingerprint duplicate
// and skipped (idempotent — no double insert, no double charge; G2).
func (svc *Service) runImportRows(ctx context.Context, jobID, instanceID string, stg stagedImport, startSucceeded, startFailed int) {
	rows := stg.rows
	total := len(rows)
	succeeded, failed := startSucceeded, startFailed
	start := startSucceeded + startFailed
	if start > total {
		start = total
	}

	var rowErrs []rowError
	truncated := false
	summary := map[string]int{}
	record := func(row int, code, msg string) {
		failed++
		summary[code]++
		if len(rowErrs) < maxImportErrors {
			rowErrs = append(rowErrs, rowError{Row: row, ID: nil, Code: code, Message: msg})
		} else {
			truncated = true
		}
	}

	// insertedThisAttempt disambiguates the two ways a row can collide with its OWN batch id: a
	// resume catch-up row (its key was committed by a PREVIOUS attempt past the durable cursor —
	// idempotent skip) vs an in-file duplicate row (its first occurrence was inserted by THIS
	// attempt — a real per-row error, exactly as a fresh run reports it). Only ids inserted in
	// this attempt live here, so the set stays O(rows-per-attempt).
	insertedThisAttempt := map[string]bool{}

	for i := start; i < total; i++ {
		r := rows[i]
		rowNo := i + 1
		if r.Secret == "" {
			record(rowNo, codeValidationFailed, "secret column empty")
		} else if envID, err := svc.secrets.Seal(ctx, "provider_key", []byte(r.Secret)); err != nil {
			record(rowNo, codeInternal, "seal failed")
		} else if dup, sameBatch, found, derr := svc.store.fingerprintDupDetail(ctx, stg.providerID, string(envID), jobID); derr != nil {
			record(rowNo, codeInternal, "duplicate check failed")
			_ = svc.store.deleteEnvelope(ctx, string(envID))
		} else if found && sameBatch && !insertedThisAttempt[dup] {
			// Committed by THIS batch on a prior attempt — idempotent resume skip (count once, no
			// second insert). The just-sealed envelope is redundant; drop it.
			succeeded++
			_ = svc.store.deleteEnvelope(ctx, string(envID))
		} else if found {
			record(rowNo, codeConflict, "duplicate of key "+dup+" by fingerprint")
			_ = svc.store.deleteEnvelope(ctx, string(envID)) // no orphan ciphertext for a rejected row
		} else if k := svc.importKey(ctx, r, stg, string(envID), jobID); svc.store.insertKey(ctx, k) != nil {
			record(rowNo, codeInternal, "insert failed")
			_ = svc.store.deleteEnvelope(ctx, string(envID))
		} else {
			succeeded++
			insertedThisAttempt[k.ID] = true
		}

		// Flush + renew + poll cancel on the wave boundary. A lost lease (owned=false) means the
		// janitor re-queued us and a successor took over — STOP without a terminal write so the
		// successor stays authoritative and the staged payload is retained for it.
		if (i+1)%bulkProgressEvery == 0 {
			owned, cancel, err := svc.store.renewBulkJobOwned(ctx, jobID, instanceID, succeeded, failed)
			if err != nil {
				svc.log.Error("import: lease renew failed", "job", jobID, "err", err)
			}
			if !owned {
				svc.log.Info("import executor superseded; stopping", "job", jobID)
				return
			}
			_ = svc.store.updateBatchProgress(ctx, jobID, succeeded, failed)
			if cancel {
				svc.finishImport(ctx, jobID, instanceID, "cancelled", succeeded, failed, rowErrs, summary, truncated, total, true)
				return
			}
		}
	}

	status := StatusImportSucceeded
	if failed > 0 {
		status = StatusImportPartial
	}
	svc.finishImport(ctx, jobID, instanceID, status, succeeded, failed, rowErrs, summary, truncated, total, true)
}

// finishImport commits the terminal bulk_jobs status (ownership-guarded) and mirrors it to the
// key_import_batches provenance record (GET /key-imports/{job_id}); on a successful terminal write
// it evicts the staged payload. A superseded executor (owned=false) leaves everything to its
// successor. cancelled retains committed rows (idempotent resubmit is safe).
func (svc *Service) finishImport(ctx context.Context, jobID, instanceID, status string, succeeded, failed int, rowErrs []rowError, summary map[string]int, truncated bool, total int, evict bool) {
	errsJSON := marshalErrors(rowErrs, summary, truncated)
	owned, err := svc.store.finishBulkJobOwned(ctx, jobID, instanceID, status, succeeded, failed, "", errsJSON, "")
	if err != nil {
		svc.log.Error("import: finish bulk_jobs failed", "job", jobID, "err", err)
	}
	if !owned {
		return // a resumed successor owns the row now
	}
	if err := svc.store.updateBatchProgress(ctx, jobID, succeeded, failed); err != nil {
		svc.log.Error("import: final progress failed", "batch", jobID, "err", err)
	}
	if err := svc.store.finishBatch(ctx, jobID, status, errsJSON); err != nil {
		svc.log.Error("import: finish batch failed", "batch", jobID, "err", err)
	}
	if evict {
		svc.stage.evict(jobID)
	}
	svc.log.Info("import complete", "batch", jobID, "status", status, "total", total, "succeeded", succeeded, "failed", failed)
}

// importKey builds the provider_keys row for one import record (stamped with the batch id so a
// resume can recognize its own committed rows via the keyed fingerprint).
func (svc *Service) importKey(ctx context.Context, r importRow, stg stagedImport, envID, batchID string) Key {
	return Key{
		ID:               newID(),
		ProviderID:       stg.providerID,
		Label:            sanitizeCell(r.Label),
		SecretEnvelopeID: envID,
		SecretLast4:      last4(r.Secret),
		Status:           StatusActive,
		Weight:           weightOr(r.Weight),
		Priority:         r.Priority,
		Region:           sanitizeCell(r.Region),
		Environment:      sanitizeCell(r.Environment),
		DailyLimit:       r.DailyLimit,
		MonthlyLimit:     r.MonthlyLimit,
		RPMLimit:         r.RPMLimit,
		OwnerTenantID:    stg.ownerTenant,
		ImportedBatchID:  batchID,
		CreatedBy:        actorFrom(ctx),
	}
}

func weightOr(w *int64) int64 {
	if w == nil {
		return 100
	}
	return *w
}

// marshalErrors renders the per-row errors + summary into the key_import_batches.errors jsonb
// shape (doc 04 §4.3). It can never contain key material — rowError.Message is caller-controlled
// and only carries codes, row numbers, and key ids.
func marshalErrors(errs []rowError, summary map[string]int, truncated bool) string {
	if len(errs) == 0 && len(summary) == 0 {
		return ""
	}
	payload := struct {
		Errors          []rowError     `json:"errors"`
		ErrorSummary    map[string]int `json:"error_summary"`
		ErrorsTruncated bool           `json:"errors_truncated"`
	}{Errors: errs, ErrorSummary: summary, ErrorsTruncated: truncated}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}
