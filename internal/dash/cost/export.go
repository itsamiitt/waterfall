package cost

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// exportBatch is the keyset page size for streaming export. Each batch is one SHORT transaction
// (never a single long-held cursor tx, doc 12 §P6) so an export can run for minutes without pinning
// a connection or a snapshot.
const exportBatch = 200

// Export streams the SAME whitelist group-by query as Summary as newline-delimited JSON (WYSIWYG,
// doc 04 §2.10). It advances a keyset cursor over the group key and issues one bounded, short
// transaction per batch via runGroup — so the connection is released between pages. Each line
// carries the dimension key plus credits/calls/successful_results and the derived ratios. The
// caller (HTTP handler) has already set Content-Type/Content-Disposition and flushes as needed.
func (s *Service) Export(ctx context.Context, w io.Writer, groupBy string, from, to time.Time, filters map[string]string, isOperator bool) error {
	q, err := buildQuery(groupBy, from, to, filters, isOperator, s.now())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	cursorKey := ""
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rows, err := s.runGroup(ctx, q, cursorKey, exportBatch)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := enc.Encode(renderRow(q.spec.keyCol, r)); err != nil {
				return err
			}
		}
		if len(rows) < exportBatch {
			return nil
		}
		cursorKey = rows[len(rows)-1].Key
	}
}

// renderRow builds the WYSIWYG line: the dimension key under its column name plus the numerator +
// both denominators and the derived (never stored) ratios.
func renderRow(keyCol string, r Row) map[string]any {
	m := map[string]any{
		keyCol:                          r.Key,
		"credits":                       r.Credits,
		"calls":                         r.Calls,
		"successful_results":            r.Success,
		"credits_per_call":              ratio(r.Credits, r.Calls),
		"credits_per_successful_result": ratio(r.Credits, r.Success),
	}
	return m
}

// ratio divides carrying nil for a zero denominator (never a divide-by-zero, never a fabricated 0).
func ratio(num, den int64) *float64 {
	if den == 0 {
		return nil
	}
	v := float64(num) / float64(den)
	return &v
}
