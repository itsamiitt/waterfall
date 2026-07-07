package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// MyEmailVerifier builds an adapter for the MyEmailVerifier single-email validation API (docs/03 §9).
//   - Endpoint: GET {base}?email=  (base default https://api.myemailverifier.com/api/validate_single.php)
//     [github.com/pat-myemailverifier/myemailverifier-api]. A path-form endpoint also exists on
//     client.myemailverifier.com; the query-param api.* form is the vendor-recommended automation one.
//   - Auth: API key in the "apikey" QUERY parameter, injected at egress (AuthAPIKeyQuery).
//   - Input: work_email. Fills: email_status (Status: Valid|Invalid|Catch-all|Unknown) + work_email
//     (the normalized/echoed Address).
//   - Quirk: success body is JSON with all boolean-like flags as STRING "true"/"false". Errors come
//     as JSON {"status":false,"error":"INVALID_API_KEY|INSUFFICIENT_CREDITS|RATE_LIMIT_EXCEEDED|…"};
//     Decode classifies the error code (insufficient→QUOTA, rate→RATE_LIMIT, else AUTH). The
//     lowercase "status" (bool, error path) is distinct from the uppercase "Status" (string, success).
//
// VERIFIED from the vendor's official docs repo: endpoint, apikey query auth, Status enum, JSON
// error shape. Exact success field names pinned UNVERIFIED until a live key (see hunter.go).
func MyEmailVerifier(base string, client *http.Client) *provider.HTTPAdapter {
	if base == "" {
		base = "https://api.myemailverifier.com/api/validate_single.php"
	}
	return &provider.HTTPAdapter{
		NameV:   "myemailverifier",
		BaseURL: base,
		Client:  client,
		Auth: provider.AuthDescriptor{
			Scheme:          provider.AuthAPIKeyQuery,
			QueryParam:      "apikey",
			KeyPoolSelector: "myemailverifier:default",
		},
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 1, ExpectedConfidence: 0.90},
			{Field: domain.FieldWorkEmail, Cost: 1, ExpectedConfidence: 0.60},
		},
		Build: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			u, err := url.Parse(base)
			if err != nil {
				return nil, err
			}
			q := u.Query()
			q.Set("email", req.Known[domain.FieldWorkEmail])
			u.RawQuery = q.Encode()
			return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		},
		Decode: func(body []byte) (provider.Result, error) {
			var p struct {
				Status  string `json:"Status"`
				Address string `json:"Address"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(body, &p); err != nil {
				return provider.Result{}, err
			}
			// JSON error body: {"status":false,"error":"<CODE>"}.
			if p.Error != "" {
				return provider.Result{}, bodyErr("myemailverifier", p.Error)
			}
			res := provider.Result{Values: map[domain.Field]provider.Observation{}}
			if p.Status != "" {
				res.Values[domain.FieldEmailStatus] = provider.Observation{Value: p.Status, Confidence: 0.90}
			}
			if p.Address != "" {
				res.Values[domain.FieldWorkEmail] = provider.Observation{Value: p.Address, Confidence: 0.60}
			}
			return res, nil
		},
	}
}
