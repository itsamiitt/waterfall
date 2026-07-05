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

// runImport processes a batch asynchronously. ctx MUST be detached from the request (a background
// context carrying the captured Principal) — the request context is cancelled when the 202
// response flushes. All persistence is Class P via PlatformTx; the captured Principal only scopes
// the audit chain. Rows commit independently (per-row seal + insert), so progress is durable.
func (svc *Service) runImport(ctx context.Context, batchID, providerID, ownerTenant string, rows []importRow) {
	total := len(rows)
	if err := svc.store.setBatchTotal(ctx, batchID, total); err != nil {
		svc.log.Error("import: set total failed", "batch", batchID, "err", err)
	}

	succeeded, failed := 0, 0
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

	for i, r := range rows {
		rowNo := i + 1
		if r.Secret == "" {
			record(rowNo, codeValidationFailed, "secret column empty")
			svc.progress(ctx, batchID, succeeded, failed, i)
			continue
		}

		// Seal immediately; plaintext never leaves this iteration.
		envID, err := svc.secrets.Seal(ctx, "provider_key", []byte(r.Secret))
		if err != nil {
			record(rowNo, codeInternal, "seal failed")
			svc.progress(ctx, batchID, succeeded, failed, i)
			continue
		}

		// Duplicate detection by keyed fingerprint — no decryption.
		if dup, found, derr := svc.store.fingerprintDup(ctx, providerID, string(envID)); derr != nil {
			record(rowNo, codeInternal, "duplicate check failed")
			_ = svc.store.deleteEnvelope(ctx, string(envID))
			svc.progress(ctx, batchID, succeeded, failed, i)
			continue
		} else if found {
			record(rowNo, codeConflict, "duplicate of key "+dup+" by fingerprint")
			_ = svc.store.deleteEnvelope(ctx, string(envID)) // no orphan ciphertext for a rejected row
			svc.progress(ctx, batchID, succeeded, failed, i)
			continue
		}

		k := Key{
			ID:               newID(),
			ProviderID:       providerID,
			Label:            sanitizeCell(r.Label),
			SecretEnvelopeID: string(envID),
			SecretLast4:      last4(r.Secret),
			Status:           StatusActive,
			Weight:           weightOr(r.Weight),
			Priority:         r.Priority,
			Region:           sanitizeCell(r.Region),
			Environment:      sanitizeCell(r.Environment),
			DailyLimit:       r.DailyLimit,
			MonthlyLimit:     r.MonthlyLimit,
			RPMLimit:         r.RPMLimit,
			OwnerTenantID:    ownerTenant,
			ImportedBatchID:  batchID,
			CreatedBy:        actorFrom(ctx),
		}
		if err := svc.store.insertKey(ctx, k); err != nil {
			record(rowNo, codeInternal, "insert failed")
			_ = svc.store.deleteEnvelope(ctx, string(envID))
			svc.progress(ctx, batchID, succeeded, failed, i)
			continue
		}
		succeeded++
		svc.progress(ctx, batchID, succeeded, failed, i)
	}

	status := StatusImportSucceeded
	if failed > 0 {
		status = StatusImportPartial
	}
	if err := svc.store.updateBatchProgress(ctx, batchID, succeeded, failed); err != nil {
		svc.log.Error("import: final progress failed", "batch", batchID, "err", err)
	}
	errsJSON := marshalErrors(rowErrs, summary, truncated)
	if err := svc.store.finishBatch(ctx, batchID, status, errsJSON); err != nil {
		svc.log.Error("import: finish failed", "batch", batchID, "err", err)
	}
	svc.log.Info("import complete", "batch", batchID, "total", total, "succeeded", succeeded, "failed", failed)
}

// progress flushes intermediate counters roughly every 50 rows (and never on the hot path more
// often), so a poller/SSE sees durable advance without a write per row.
func (svc *Service) progress(ctx context.Context, batchID string, succeeded, failed, i int) {
	if (i+1)%50 != 0 {
		return
	}
	if err := svc.store.updateBatchProgress(ctx, batchID, succeeded, failed); err != nil {
		svc.log.Error("import: progress flush failed", "batch", batchID, "err", err)
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
