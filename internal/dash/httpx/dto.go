package httpx

import (
	"encoding/json"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/security"
)

// decodeJSON strictly decodes an application/json body (DisallowUnknownFields + MaxBytesReader),
// writing a 400 invalid_json and returning false on any failure (doc 04 §1.1).
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "request body is not valid JSON")
		return false
	}
	return true
}

// --- request bodies ---

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type codeRequest struct {
	Code string `json:"code"`
}

type userCreateRequest struct {
	Email    string `json:"email"`
	Role     string `json:"role"`
	Password string `json:"password"` // optional; unset => reset flow required
}

type userPatchRequest struct {
	Role   string `json:"role"`
	Status string `json:"status"`
}

type ipAllowlistRequest struct {
	Rules []struct {
		CIDR  string `json:"cidr"`
		Label string `json:"label"`
	} `json:"rules"`
}

// --- response bodies ---

type userSummary struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	TenantID    string `json:"tenant_id"`
	MFAEnrolled bool   `json:"mfa_enrolled"`
}

func toUserSummary(u security.User) userSummary {
	return userSummary{
		ID: u.ID, Email: u.Email, Role: u.Role, TenantID: u.TenantID, MFAEnrolled: u.MFAEnrolled,
	}
}

type sessionOK struct {
	Status    string      `json:"status"`
	CSRFToken string      `json:"csrf_token"`
	User      userSummary `json:"user"`
}

// listEnvelope is the uniform paginated list response (doc 04 §1.4).
type listEnvelope struct {
	Items      any     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func cursorOut(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
