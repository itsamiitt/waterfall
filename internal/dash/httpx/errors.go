// Package httpx is the dashboard's HTTP surface plumbing (doc 04, doc 05 §4): the session-or-JWT
// authenticator, the CSRF / IP-allowlist / RBAC / idempotency middleware chain, the audited-write
// wrapper, and the Server that mounts the P0 /v1/admin routes. It owns no tenant state — every
// handler derives tenant and role from the verified Principal (gate G1) and delegates persistence
// to internal/dash/security, /audit, and /secrets.
package httpx

import (
	"encoding/json"
	"net/http"
)

// Error codes (doc 04 §1.6 registry). The vocabulary is closed; these are the P0 subset plus
// csrf_required (missing CSRF header) which reconciles doc 04 §1.6 with the doc 12 P0 acceptance
// criterion #4 — csrf_invalid remains for a present-but-mismatched token.
const (
	codeInvalidJSON      = "invalid_json"
	codeMissingIdemKey   = "missing_idempotency_key"
	codeInvalidCursor    = "invalid_cursor"
	codeInvalidFilter    = "invalid_filter"
	codeUnauthorized     = "unauthorized"
	codeMFARequired      = "mfa_required"
	codeForbidden        = "forbidden"
	codeCSRFRequired     = "csrf_required"
	codeCSRFInvalid      = "csrf_invalid"
	codeIPNotAllowed     = "ip_not_allowed"
	codeNotFound         = "not_found"
	codeIdempotencyReuse = "idempotency_key_reuse"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codeInternal         = "internal"
)

// writeJSON encodes v as an application/json response with the given status.
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

// writeError emits the uniform error envelope (identical shape to internal/api.writeError).
func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}
