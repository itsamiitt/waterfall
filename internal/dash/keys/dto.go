package keys

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// Wire DTOs + shared HTTP helpers for module 3. Response DTOs carry ONLY display-safe fields:
// secret_last4 and the fingerprint prefix identify a key; the plaintext and ciphertext never
// appear (doc 05 §7.3). This file also re-implements the small JSON/cursor helpers locally so the
// keys package takes no dependency on internal/dash/httpx (coordinate avoidance).

// --- request bodies ---

type createKeyReq struct {
	Label            string   `json:"label"`
	Secret           string   `json:"secret"`
	AuthMethod       string   `json:"auth_method"`
	Weight           *int64   `json:"weight"`
	Priority         *int64   `json:"priority"`
	Region           string   `json:"region"`
	Environment      string   `json:"environment"`
	Team             string   `json:"team"`
	Owner            string   `json:"owner"`
	Notes            string   `json:"notes"`
	RotationGroup    string   `json:"rotation_group"`
	DailyLimit       *int64   `json:"daily_limit"`
	MonthlyLimit     *int64   `json:"monthly_limit"`
	RPMLimit         *int64   `json:"rpm_limit"`
	ConcurrencyLimit *int64   `json:"concurrency_limit"`
	ExpiresAt        string   `json:"expires_at"`
	PoolIDs          []string `json:"pool_ids"`
	Tags             []string `json:"tags"`
}

// patchKeyReq mirrors KeyPatch field-for-field (same types, order) so KeyPatch(req) converts.
type patchKeyReq struct {
	Label            *string   `json:"label"`
	AuthMethod       *string   `json:"auth_method"`
	Weight           *int64    `json:"weight"`
	Priority         *int64    `json:"priority"`
	Region           *string   `json:"region"`
	Environment      *string   `json:"environment"`
	Team             *string   `json:"team"`
	Owner            *string   `json:"owner"`
	Notes            *string   `json:"notes"`
	DailyLimit       *int64    `json:"daily_limit"`
	MonthlyLimit     *int64    `json:"monthly_limit"`
	RPMLimit         *int64    `json:"rpm_limit"`
	ConcurrencyLimit *int64    `json:"concurrency_limit"`
	RotationGroup    *string   `json:"rotation_group"`
	Tags             *[]string `json:"tags"`
	ExpiresAt        *string   `json:"expires_at"`
}

type poolReq struct {
	ProviderID     string          `json:"provider_id"`
	Name           string          `json:"name"`
	Strategy       string          `json:"strategy"`
	StrategyParams json.RawMessage `json:"strategy_params"`
}

type bulkReq struct {
	IDs     []string    `json:"ids"`
	Filter  *bulkFilter `json:"filter"`
	Op      string      `json:"op"`
	Reason  string      `json:"reason"`
	Preview bool        `json:"preview"`
}

type bulkFilter struct {
	ProviderID      string   `json:"provider_id"`
	Status          []string `json:"status"`
	Region          string   `json:"region"`
	Environment     string   `json:"environment"`
	RotationGroup   string   `json:"rotation_group"`
	ImportedBatchID string   `json:"imported_batch_id"`
	Tag             string   `json:"tag"`
	PoolID          string   `json:"pool_id"`
}

// --- response bodies ---

type keyDTO struct {
	ID                 string   `json:"id"`
	ProviderID         string   `json:"provider_id"`
	Label              string   `json:"label,omitempty"`
	Status             string   `json:"status"`
	Health             string   `json:"health,omitempty"`
	SecretLast4        string   `json:"secret_last4,omitempty"`
	FingerprintPrefix  string   `json:"fingerprint_prefix,omitempty"`
	SecretEnvelopeID   string   `json:"secret_envelope_id"`
	AuthMethod         string   `json:"auth_method,omitempty"`
	Weight             int64    `json:"weight"`
	Priority           *int64   `json:"priority,omitempty"`
	Region             string   `json:"region,omitempty"`
	Environment        string   `json:"environment,omitempty"`
	Team               string   `json:"team,omitempty"`
	Owner              string   `json:"owner,omitempty"`
	DailyLimit         *int64   `json:"daily_limit,omitempty"`
	MonthlyLimit       *int64   `json:"monthly_limit,omitempty"`
	RPMLimit           *int64   `json:"rpm_limit,omitempty"`
	ConcurrencyLimit   *int64   `json:"concurrency_limit,omitempty"`
	CreditsRemaining   *int64   `json:"credits_remaining,omitempty"`
	ExpiresAt          string   `json:"expires_at,omitempty"`
	OwnerTenantID      string   `json:"owner_tenant_id,omitempty"`
	RotationGroup      string   `json:"rotation_group,omitempty"`
	ImportedBatchID    string   `json:"imported_batch_id,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	RotatedTo          string   `json:"rotated_to,omitempty"`
	RotateOverlapUntil string   `json:"rotate_overlap_until,omitempty"`
	LastUsedAt         string   `json:"last_used_at,omitempty"`
	LastRotatedAt      string   `json:"last_rotated_at,omitempty"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

func toKeyDTO(k Key, fingerprintPrefix string) keyDTO {
	return keyDTO{
		ID: k.ID, ProviderID: k.ProviderID, Label: k.Label, Status: k.Status, Health: k.Health,
		SecretLast4: k.SecretLast4, FingerprintPrefix: fingerprintPrefix, SecretEnvelopeID: k.SecretEnvelopeID,
		AuthMethod: k.AuthMethod, Weight: k.Weight, Priority: k.Priority,
		Region: k.Region, Environment: k.Environment, Team: k.Team, Owner: k.Owner,
		DailyLimit: k.DailyLimit, MonthlyLimit: k.MonthlyLimit, RPMLimit: k.RPMLimit,
		ConcurrencyLimit: k.ConcurrencyLimit, CreditsRemaining: k.CreditsRemaining,
		ExpiresAt: k.ExpiresAt, OwnerTenantID: k.OwnerTenantID, RotationGroup: k.RotationGroup,
		ImportedBatchID: k.ImportedBatchID, Tags: k.Tags, RotatedTo: k.RotatedTo,
		RotateOverlapUntil: k.RotateOverlapUntil, LastUsedAt: k.LastUsedAt, LastRotatedAt: k.LastRotatedAt,
		CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt,
	}
}

type poolDTO struct {
	ID             string          `json:"id"`
	ProviderID     string          `json:"provider_id"`
	Name           string          `json:"name"`
	Selector       string          `json:"selector"`
	Strategy       string          `json:"strategy"`
	StrategyParams json.RawMessage `json:"strategy_params,omitempty"`
	OwnerTenantID  string          `json:"owner_tenant_id,omitempty"`
	Status         string          `json:"status"`
	MemberCount    int             `json:"member_count"`
	CreatedAt      string          `json:"created_at"`
}

func toPoolDTO(p Pool) poolDTO {
	var params json.RawMessage
	if p.StrategyParams != "" && json.Valid([]byte(p.StrategyParams)) {
		params = json.RawMessage(p.StrategyParams)
	}
	return poolDTO{
		ID: p.ID, ProviderID: p.ProviderID, Name: p.Name, Selector: p.Selector(),
		Strategy: p.Strategy, StrategyParams: params, OwnerTenantID: p.OwnerTenantID,
		Status: p.Status, MemberCount: p.MemberCount, CreatedAt: p.CreatedAt,
	}
}

// toImportDTO renders the §4.3 progress schema. The errors sub-document is passed through from the
// stored jsonb (already redacted — codes/rows/ids only).
func toImportDTO(b ImportBatch) map[string]any {
	out := map[string]any{
		"job_id": b.ID, "kind": "key_import", "status": b.Status,
		"total": b.Total, "succeeded": b.Succeeded, "failed": b.Failed,
		"started_at": nullString(b.CreatedAt), "finished_at": nullString(b.FinishedAt),
		"matched_at_execution": nil,
	}
	var errs struct {
		Errors          []rowError     `json:"errors"`
		ErrorSummary    map[string]int `json:"error_summary"`
		ErrorsTruncated bool           `json:"errors_truncated"`
	}
	if b.Errors != "" {
		_ = json.Unmarshal([]byte(b.Errors), &errs)
	}
	if errs.Errors == nil {
		errs.Errors = []rowError{}
	}
	if errs.ErrorSummary == nil {
		errs.ErrorSummary = map[string]int{}
	}
	out["errors"] = errs.Errors
	out["error_summary"] = errs.ErrorSummary
	out["errors_truncated"] = errs.ErrorsTruncated
	return out
}

// listEnvelope is the uniform paginated list response (doc 04 §1.4).
type listEnvelope struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// --- shared HTTP helpers (kept local; no httpx dependency) ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

// decodeJSON strictly decodes a bounded JSON body (DisallowUnknownFields + MaxBytesReader),
// writing 400 invalid_json and returning false on failure (doc 04 §1.1).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

// optionalJSON decodes an optional body (an empty body is not an error).
func optionalJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return nil
}

// parseCursor decodes ?cursor= (opaque base64url); a bad cursor is 400 invalid_cursor.
func parseCursor(w http.ResponseWriter, r *http.Request) (db.Cursor, bool) {
	cur, err := db.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidCursor, "cursor is not decodable")
		return db.Cursor{}, false
	}
	return cur, true
}

// parseLimit decodes ?limit=; out-of-range is 400 invalid_filter (clamped downstream anyway).
func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		writeError(w, http.StatusBadRequest, codeInvalidFilter, "limit must be between 1 and 200")
		return 0, false
	}
	return n, true
}

func encodeCursor(c db.Cursor) string {
	if len(c.K) == 0 && c.ID == "" {
		return ""
	}
	return db.EncodeCursor(c)
}

func cursorOut(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullString renders "" as JSON null (a real timestamp/value otherwise).
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// rawToString renders a JSON body fragment as a compact string, treating absent/"null" as "".
func rawToString(raw json.RawMessage) string {
	s := string(raw)
	if s == "" || s == "null" {
		return ""
	}
	return s
}
