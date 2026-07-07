package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/provider"
)

// Verifalia builds an async adapter for the Verifalia v2.6 email-validation API (docs/03 §7) — the
// first submit→poll consumer of the ADR-0024 AsyncHTTPAdapter.
//   - Submit: POST {base}/email-validations?waitTime=0 with {"entries":[{"inputData":<email>}]}
//     (waitTime=0 forces the async path) → overview.id.
//   - Poll: GET {base}/email-validations/{id} until overview.status=="Completed" (HTTP 202 while
//     InProgress; 200 when done). Expired/Deleted = terminal-gone → NOT_FOUND.
//   - Auth: HTTP Basic (pool secret "username:password"), injected at egress.
//   - base default https://api.verifalia.com/v2.6 [verifalia.com/developers].
//
// VERIFIED from docs: endpoints, basic auth, overview.id/status + entries.data[].classification.
// Field names pinned UNVERIFIED until a live authorized call (see hunter.go).
func Verifalia(base string, client *http.Client) *provider.AsyncHTTPAdapter {
	if base == "" {
		base = "https://api.verifalia.com/v2.6"
	}
	return &provider.AsyncHTTPAdapter{
		NameV:        "verifalia",
		BaseURL:      base,
		Client:       client,
		Auth:         provider.AuthDescriptor{Scheme: provider.AuthBasic, KeyPoolSelector: "verifalia:default"},
		Policy:       provider.CallPolicy{Timeout: 30 * time.Second, MaxAttempts: 1},
		PollInterval: 2 * time.Second,
		Caps: []provider.Capability{
			{Field: domain.FieldEmailStatus, Cost: 3, ExpectedConfidence: 0.95},
			{Field: domain.FieldWorkEmail, Cost: 3, ExpectedConfidence: 0.90},
		},
		Submit: func(ctx context.Context, base string, req provider.Request) (*http.Request, error) {
			body := map[string]any{"entries": []map[string]string{{"inputData": req.Known[domain.FieldWorkEmail]}}}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/email-validations?waitTime=0", bytes.NewReader(b))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			return r, nil
		},
		ParseSubmit: parseVerifaliaID,
		Poll: func(ctx context.Context, base, token string) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, base+"/email-validations/"+token, nil)
		},
		Decode: decodeVerifalia,
	}
}

func parseVerifaliaID(body []byte) (string, error) {
	var p struct {
		Overview struct {
			ID string `json:"id"`
		} `json:"overview"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	if p.Overview.ID == "" {
		return "", domain.NewProviderError("verifalia", domain.ClassTransient, errNoJobID)
	}
	return p.Overview.ID, nil
}

func decodeVerifalia(body []byte) (provider.Result, bool, error) {
	var p struct {
		Overview struct {
			Status string `json:"status"`
		} `json:"overview"`
		Entries struct {
			Data []struct {
				EmailAddress   string `json:"emailAddress"`
				Classification string `json:"classification"`
			} `json:"data"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return provider.Result{}, false, err
	}
	switch p.Overview.Status {
	case "Completed":
		// terminal — fall through to mapping
	case "Expired", "Deleted":
		return provider.Result{}, false, domain.NewProviderError("verifalia", domain.ClassNotFound, errResultsGone)
	default: // InProgress or empty — keep polling
		return provider.Result{}, false, nil
	}
	res := provider.Result{Values: map[domain.Field]provider.Observation{}}
	if len(p.Entries.Data) > 0 {
		e := p.Entries.Data[0]
		if e.Classification != "" {
			res.Values[domain.FieldEmailStatus] = provider.Observation{Value: strings.ToLower(e.Classification), Confidence: 0.95}
		}
		if e.EmailAddress != "" {
			res.Values[domain.FieldWorkEmail] = provider.Observation{Value: e.EmailAddress, Confidence: 0.90}
		}
	}
	return res, true, nil
}
