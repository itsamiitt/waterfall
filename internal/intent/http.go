package intent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/enrichment/waterfall/internal/tenant"
)

// ScoreStore reads computed intent scores. *Store satisfies it; a nil Store disables the intent API
// (GET /v1/intent/accounts/{domain} then returns 404).
type ScoreStore interface {
	GetByAccount(ctx context.Context, account string) ([]ClassScore, error)
}

// HTTPHandler serves the intent read API (ADR-0027) on the enrichapi deployable. The tenant flows
// from the authenticated Principal (G1, never the path); errors use the uniform body
// {"error":{"code","message"}}.
type HTTPHandler struct {
	Store ScoreStore // optional; enables GET /v1/intent/accounts/{domain}
}

// accountResponse is the GET /v1/intent/accounts/{domain} body: the per-class scores for an account.
type accountResponse struct {
	Account string       `json:"account"`
	Scores  []ClassScore `json:"scores"`
}

// Routes registers the intent read endpoints on a standalone mux (tests / standalone serving). The
// mounting gateway instead sets api.Server.Intent = h and applies its own auth/rate-limit wrappers.
func (h *HTTPHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/intent/accounts/{domain}", h.Accounts)
}

// Accounts handles GET /v1/intent/accounts/{domain}: the last-computed per-class intent scores for a
// Company/account within the caller's tenant (intent is computed on a separate async lane, ADR-0027,
// so this reads what the last refresh stored). An account with no computed intent returns 200 with an
// empty score list — that is a valid "no intent yet" answer, not a 404. 404 only when persistence is
// disabled.
func (h *HTTPHandler) Accounts(w http.ResponseWriter, r *http.Request) {
	if _, err := tenant.FromContext(r.Context()); err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no principal")
		return
	}
	if h.Store == nil {
		writeErr(w, http.StatusNotFound, "not_found", "intent persistence is not enabled")
		return
	}
	account := strings.TrimSpace(r.PathValue("domain"))
	if account == "" {
		writeErr(w, http.StatusUnprocessableEntity, "validation_error", "domain is required")
		return
	}
	scores, err := h.Store.GetByAccount(r.Context(), account)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "intent lookup failed")
		return
	}
	if scores == nil {
		scores = []ClassScore{}
	}
	writeJSON(w, http.StatusOK, accountResponse{Account: account, Scores: scores})
}

// --- uniform response helpers (ADR-0012) ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	type errObj struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	writeJSON(w, status, struct {
		Error errObj `json:"error"`
	}{Error: errObj{Code: code, Message: msg}})
}
