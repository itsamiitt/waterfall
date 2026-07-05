package keys

import (
	"strconv"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// pgStore is the Class-P persistence layer for module 3. Every method runs through
// db.Store.PlatformTx (tenant='platform'), the one-owner path for provider_keys / key_pools /
// key_pool_members / key_import_batches — no tenant-scoped transaction can reach these tables
// except through the enumerated BYO read projections in RLS (doc 05 §3.2). Concrete SQL lives in
// pgstore.go; this file holds the type, the column projections, and the text<->Go helpers the
// internal/pg text-format client needs.
type pgStore struct {
	db *db.Store
}

func newPGStore(store *db.Store) *pgStore { return &pgStore{db: store} }

// keyColumns is the ordered provider_keys projection shared by every read path; scanKey maps a
// row positionally, so the two MUST stay in lockstep.
const keyColumns = `id, provider_id, label, secret_envelope_id, secret_last4, auth_method, status,
	disable_reason, health, weight, priority, region, environment, team, owner, notes,
	daily_limit, monthly_limit, rpm_limit, concurrency_limit, credits_remaining, credits_used,
	expires_at, owner_tenant_id, rotation_group, imported_batch_id, tags, rotated_to,
	rotate_overlap_until, last_used_at, last_rotated_at, created_by, created_at, updated_at`

// scanKey maps a keyColumns row into a Key.
func scanKey(r []*string) Key {
	return Key{
		ID:                 s(r[0]),
		ProviderID:         s(r[1]),
		Label:              s(r[2]),
		SecretEnvelopeID:   s(r[3]),
		SecretLast4:        s(r[4]),
		AuthMethod:         s(r[5]),
		Status:             s(r[6]),
		DisableReason:      s(r[7]),
		Health:             s(r[8]),
		Weight:             i64(r[9]),
		Priority:           pint(r[10]),
		Region:             s(r[11]),
		Environment:        s(r[12]),
		Team:               s(r[13]),
		Owner:              s(r[14]),
		Notes:              s(r[15]),
		DailyLimit:         pint(r[16]),
		MonthlyLimit:       pint(r[17]),
		RPMLimit:           pint(r[18]),
		ConcurrencyLimit:   pint(r[19]),
		CreditsRemaining:   pint(r[20]),
		CreditsUsed:        pint(r[21]),
		ExpiresAt:          s(r[22]),
		OwnerTenantID:      s(r[23]),
		RotationGroup:      s(r[24]),
		ImportedBatchID:    s(r[25]),
		Tags:               parsePGArray(r[26]),
		RotatedTo:          s(r[27]),
		RotateOverlapUntil: s(r[28]),
		LastUsedAt:         s(r[29]),
		LastRotatedAt:      s(r[30]),
		CreatedBy:          s(r[31]),
		CreatedAt:          s(r[32]),
		UpdatedAt:          s(r[33]),
	}
}

const poolColumns = `id, provider_id, name, strategy, strategy_params, owner_tenant_id, status, created_at`

func scanPool(r []*string) Pool {
	return Pool{
		ID:             s(r[0]),
		ProviderID:     s(r[1]),
		Name:           s(r[2]),
		Strategy:       s(r[3]),
		StrategyParams: s(r[4]),
		OwnerTenantID:  s(r[5]),
		Status:         s(r[6]),
		CreatedAt:      s(r[7]),
	}
}

const batchColumns = `id, provider_id, source, total, succeeded, failed, errors, status, created_by, created_at, finished_at`

func scanBatch(r []*string) ImportBatch {
	return ImportBatch{
		ID:         s(r[0]),
		ProviderID: s(r[1]),
		Source:     s(r[2]),
		Total:      int(i64(r[3])),
		Succeeded:  int(i64(r[4])),
		Failed:     int(i64(r[5])),
		Errors:     s(r[6]),
		Status:     s(r[7]),
		CreatedBy:  s(r[8]),
		CreatedAt:  s(r[9]),
		FinishedAt: s(r[10]),
	}
}

// --- text-format column helpers (the internal/pg client speaks text only) ---

// s dereferences a nullable text column to "" on NULL.
func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// i64 parses a non-null integer column, defaulting to 0.
func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	return n
}

// pint parses a nullable integer column into *int64 (nil on NULL).
func pint(p *string) *int64 {
	if p == nil {
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(*p), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// nullText renders "" as SQL NULL, else the string.
func nullText(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullInt renders a nil *int64 as SQL NULL, else the value.
func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// parsePGArray parses a Postgres text-format array literal ({a,b,"c d"}) into a slice; NULL and
// {} both yield nil.
func parsePGArray(p *string) []string {
	if p == nil {
		return nil
	}
	str := strings.TrimSpace(*p)
	if len(str) < 2 || str[0] != '{' || str[len(str)-1] != '}' {
		return nil
	}
	str = str[1 : len(str)-1]
	if str == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQ, esc := false, false
	for i := 0; i < len(str); i++ {
		c := str[i]
		switch {
		case esc:
			cur.WriteByte(c)
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			inQ = !inQ
		case c == ',' && !inQ:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// encodePGArray renders a slice as a Postgres text-format array literal, or NULL for an empty
// slice. Each element is quoted and its quotes/backslashes escaped, so a value carrying a comma
// or brace cannot break the literal.
func encodePGArray(vals []string) any {
	if len(vals) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range vals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
