package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/tenant"
)

const (
	rulesBase    = "/v1/admin/alerts/rules"
	channelsBase = "/v1/admin/alerts/channels"
	eventsBase   = "/v1/admin/alerts/events"
	maxBodyBytes = 1 << 20
)

// Error codes (doc 04 §1.6 registry + doc 12 §P6 egress_blocked).
const (
	codeInvalidJSON      = "invalid_json"
	codeUnauthorized     = "unauthorized"
	codeMFARequired      = "mfa_required"
	codeForbidden        = "forbidden"
	codeNotFound         = "not_found"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codeEgressBlocked    = "egress_blocked"
	codeInternal         = "internal"
)

// Authenticator resolves a request into a verified Principal (consumer-side; satisfied by
// httpx.CtxAuthenticator). This package never imports httpx.
type Authenticator interface {
	Authenticate(r *http.Request) (tenant.Principal, error)
}

// StepUpVerifier verifies a per-request X-MFA-Code for channel creation (doc 05 §5.4). Satisfied by
// the orchestrator's TOTP step-up adapter.
type StepUpVerifier interface {
	VerifyStepUp(ctx context.Context, code string) error
}

// Deps bundles the alerts HTTP surface collaborators.
type Deps struct {
	Service *Service
	Auth    Authenticator
	StepUp  StepUpVerifier
	Logger  *slog.Logger
}

type handlers struct {
	svc    *Service
	auth   Authenticator
	stepUp StepUpVerifier
	log    *slog.Logger
}

// Routes mounts the 10 alerts endpoints (doc 04 §2.11). CSRF / MFA-gate / IP-allowlist come from the
// shared httpx FeatureChain when co-mounted; this package owns RBAC, step-up (channel create), and
// audit. The orchestrator adds "alerts" to the feature-prefix list + featureLabels and wires Deps.
func Routes(mux *http.ServeMux, d Deps) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &handlers{svc: d.Service, auth: d.Auth, stepUp: d.StepUp, log: log}

	mux.HandleFunc("GET "+rulesBase, h.read(rbac.AlertsCRUD, h.listRules))
	mux.HandleFunc("POST "+rulesBase, h.write(rbac.AlertsCRUD, h.createRule))
	mux.HandleFunc("GET "+rulesBase+"/{id}", h.read(rbac.AlertsCRUD, h.getRule))
	mux.HandleFunc("PATCH "+rulesBase+"/{id}", h.write(rbac.AlertsCRUD, h.patchRule))
	mux.HandleFunc("DELETE "+rulesBase+"/{id}", h.write(rbac.AlertsCRUD, h.deleteRule))
	mux.HandleFunc("POST "+rulesBase+"/{id}/test", h.write(rbac.AlertsCRUD, h.testRule))

	mux.HandleFunc("GET "+channelsBase, h.read(rbac.AlertsCRUD, h.listChannels))
	mux.HandleFunc("POST "+channelsBase, h.stepUpWrite(rbac.AlertsCRUD, h.createChannel))
	mux.HandleFunc("DELETE "+channelsBase+"/{id}", h.write(rbac.AlertsCRUD, h.deleteChannel))
	mux.HandleFunc("POST "+channelsBase+"/{id}/test", h.write(rbac.AlertsCRUD, h.testChannel))

	mux.HandleFunc("GET "+eventsBase, h.read(rbac.AlertsCRUD, h.listEvents))
	mux.HandleFunc("POST "+eventsBase+"/{id}/ack", h.write(rbac.AlertsAck, h.ackEvent))
}

// --- middleware ---

func (h *handlers) read(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

func (h *handlers) write(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, next))
}

// stepUpWrite adds the X-MFA-Code step-up required for channel creation (secrets sealed, doc 05 §5.4).
func (h *handlers) stepUpWrite(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return h.authenticate(h.requireRole(action, h.requireStepUp(next)))
}

func (h *handlers) authenticate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if _, err := tenant.FromContext(ctx); err != nil {
			if h.auth == nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			p, err := h.auth.Authenticate(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid credential")
				return
			}
			ctx = tenant.WithPrincipal(ctx, p)
		}
		next(w, r.WithContext(ctx))
	}
}

func (h *handlers) requireRole(action rbac.Action, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := tenant.FromContext(r.Context())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
			return
		}
		if !rbac.Can(db.RoleFromPrincipal(p), action).Allowed() {
			writeError(w, http.StatusForbidden, codeForbidden, "role does not permit this action")
			return
		}
		next(w, r)
	}
}

func (h *handlers) requireStepUp(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.Header.Get("X-MFA-Code")
		if code == "" {
			writeError(w, http.StatusUnauthorized, codeMFARequired, "X-MFA-Code is required")
			return
		}
		if h.stepUp != nil && h.stepUp.VerifyStepUp(r.Context(), code) != nil {
			writeError(w, http.StatusUnauthorized, codeMFARequired, "X-MFA-Code missing or invalid")
			return
		}
		next(w, r)
	}
}

// --- rule handlers ---

func (h *handlers) listRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var enabled *bool
	if v := q.Get("enabled"); v != "" {
		b := v == "true"
		enabled = &b
	}
	items, err := h.svc.ListRules(r.Context(), q.Get("metric"), q.Get("severity"), enabled)
	if err != nil {
		h.fail(w, "list_rules", err)
		return
	}
	if items == nil {
		items = []Rule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) createRule(w http.ResponseWriter, r *http.Request) {
	var body Rule
	if !decodeJSON(w, r, &body) {
		return
	}
	out, err := h.svc.CreateRule(r.Context(), body)
	if err != nil {
		h.fail(w, "create_rule", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *handlers) getRule(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.GetRule(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "get_rule", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) patchRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name                *string    `json:"name"`
		Threshold           *float64   `json:"threshold"`
		WindowS             *int       `json:"window_s"`
		CooldownS           *int       `json:"cooldown_s"`
		Severity            *string    `json:"severity"`
		Channels            []string   `json:"channels"`
		Enabled             *bool      `json:"enabled"`
		MutedUntil          *time.Time `json:"muted_until"`
		AnomalyFloorCredits *int64     `json:"anomaly_floor_credits"`
		ClearMuted          bool       `json:"clear_muted"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p := RulePatch{
		Name: body.Name, Threshold: body.Threshold, WindowS: body.WindowS, CooldownS: body.CooldownS,
		Severity: body.Severity, Channels: body.Channels, Enabled: body.Enabled,
		AnomalyFloorCredits: body.AnomalyFloorCredits,
	}
	if body.MutedUntil != nil {
		p.SetMuted = true
		p.MutedUntil = body.MutedUntil
	} else if body.ClearMuted {
		p.SetMuted = true
	}
	out, err := h.svc.PatchRule(r.Context(), r.PathValue("id"), p)
	if err != nil {
		h.fail(w, "patch_rule", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) deleteRule(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteRule(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, "delete_rule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) testRule(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.TestRule(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "test_rule", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- channel handlers ---

func (h *handlers) listChannels(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.ListChannels(r.Context())
	if err != nil {
		h.fail(w, "list_channels", err)
		return
	}
	if items == nil {
		items = []Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) createChannel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string        `json:"kind"`
		Name   string        `json:"name"`
		Config ChannelConfig `json:"config"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ch, err := h.svc.CreateChannel(r.Context(), body.Kind, body.Name, body.Config)
	if err != nil {
		h.fail(w, "create_channel", err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (h *handlers) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteChannel(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, "delete_channel", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) testChannel(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.TestChannel(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, "test_channel", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- event handlers ---

func (h *handlers) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTimeQ(q.Get("from"))
	to := parseTimeQ(q.Get("to"))
	limit := 0
	if v := q.Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	items, err := h.svc.ListEvents(r.Context(), q.Get("state"), q.Get("rule_id"), q.Get("severity"), from, to, limit)
	if err != nil {
		h.fail(w, "list_events", err)
		return
	}
	if items == nil {
		items = []Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handlers) ackEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "event not found")
		return
	}
	ev, err := h.svc.AckEvent(r.Context(), id)
	if err != nil {
		h.fail(w, "ack_event", err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// --- error mapping + helpers ---

func (h *handlers) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, ErrChannelRef):
		writeError(w, http.StatusConflict, codeConflict, "channel is referenced by an enabled rule")
	case errors.Is(err, ErrEgressBlocked):
		writeError(w, http.StatusForbidden, codeEgressBlocked, "egress blocked by SSRF policy")
	case errors.Is(err, ErrUnknownMetric), errors.Is(err, ErrInvalidScope), errors.Is(err, ErrInvalidOp),
		errors.Is(err, ErrUnknownChannel), errors.Is(err, ErrInvalidChannelKind):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, err.Error())
	case errors.Is(err, tenant.ErrNoPrincipal):
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing principal")
	default:
		h.log.Error("alerts handler failed", "op", op, "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

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

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

func parseTimeQ(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
