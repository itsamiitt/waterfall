package api

import (
	"strings"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/job"
)

// submitRequest is the JSON body of POST /v1/enrichments. Note: it does NOT and cannot
// carry a tenant_id — tenant identity comes only from the authenticated principal (G1).
type submitRequest struct {
	Subject struct {
		ID    string            `json:"id"`
		Known map[string]string `json:"known"`
	} `json:"subject"`
	Want             []string `json:"want"`
	ConfidenceTarget float64  `json:"confidence_target"`
	CostCeiling      int64    `json:"cost_ceiling"`
	ConfigVersion    string   `json:"config_version"`
	Priority         string   `json:"priority"` // "premium" | "bulk" (default bulk)
}

// toDomain validates the body and builds an EnrichmentRequest, returning a human-readable
// error string (empty on success) so the handler can emit a 422 with a clear message.
func (b submitRequest) toDomain() (domain.EnrichmentRequest, string) {
	if strings.TrimSpace(b.Subject.ID) == "" {
		return domain.EnrichmentRequest{}, "subject.id is required"
	}
	if len(b.Want) == 0 {
		return domain.EnrichmentRequest{}, "want must list at least one field"
	}
	want := make([]domain.Field, 0, len(b.Want))
	for _, w := range b.Want {
		f := domain.Field(w)
		if !f.Valid() {
			return domain.EnrichmentRequest{}, "unknown field in want: " + w
		}
		want = append(want, f)
	}
	if b.CostCeiling <= 0 {
		return domain.EnrichmentRequest{}, "cost_ceiling must be > 0"
	}
	if b.ConfidenceTarget < 0 || b.ConfidenceTarget > 1 {
		return domain.EnrichmentRequest{}, "confidence_target must be in [0,1]"
	}
	known := make(map[domain.Field]string, len(b.Subject.Known))
	for k, v := range b.Subject.Known {
		f := domain.Field(k)
		if !f.Valid() {
			return domain.EnrichmentRequest{}, "unknown field in subject.known: " + k
		}
		known[f] = v
	}
	cfg := b.ConfigVersion
	if cfg == "" {
		cfg = "v1"
	}
	return domain.EnrichmentRequest{
		Subject:          domain.Subject{ID: b.Subject.ID, Known: known},
		Want:             want,
		ConfidenceTarget: domain.Confidence(b.ConfidenceTarget),
		CostCeiling:      domain.Credits(b.CostCeiling),
		ConfigVersion:    cfg,
	}, ""
}

func (b submitRequest) priority() job.Priority {
	if strings.EqualFold(b.Priority, "premium") {
		return job.PriorityPremium
	}
	return job.PriorityBulk
}

// fieldDTO is the JSON representation of a resolved FieldValue with its provenance.
type fieldDTO struct {
	Value          string  `json:"value"`
	Confidence     float64 `json:"confidence"`
	Provider       string  `json:"provider"`
	CostCredits    int64   `json:"cost_credits"`
	IdempotencyKey string  `json:"idempotency_key"`
	ObservedAt     string  `json:"observed_at"`
}

// jobResponse is the JSON representation of a Job's state and (when finished) its outcome.
type jobResponse struct {
	JobID     string              `json:"job_id"`
	Status    string              `json:"status"`
	Committed int64               `json:"committed_credits,omitempty"`
	Filled    map[string]fieldDTO `json:"filled,omitempty"`
	Stops     map[string]string   `json:"stops,omitempty"`
	Error     string              `json:"error,omitempty"`
}

func toJobResponse(j *job.Job) jobResponse {
	resp := jobResponse{JobID: j.ID, Status: string(j.Status), Error: j.Err}
	if j.Outcome != nil {
		resp.Committed = int64(j.Outcome.Committed)
		resp.Filled = map[string]fieldDTO{}
		for f, v := range j.Outcome.Filled {
			resp.Filled[string(f)] = fieldDTO{
				Value:          v.Value,
				Confidence:     float64(v.Confidence),
				Provider:       v.Prov.Provider,
				CostCredits:    int64(v.Prov.CostCredits),
				IdempotencyKey: v.Prov.IdempotencyKey,
				ObservedAt:     v.Prov.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
		resp.Stops = map[string]string{}
		for f, s := range j.Outcome.Stops {
			resp.Stops[string(f)] = string(s)
		}
	}
	return resp
}

// recordsResponse is the JSON body of GET /v1/records/{subjectID}.
type recordsResponse struct {
	SubjectID string              `json:"subject_id"`
	Fields    map[string]fieldDTO `json:"fields"`
}

func toRecordsResponse(subjectID string, cur map[domain.Field]domain.FieldValue) recordsResponse {
	resp := recordsResponse{SubjectID: subjectID, Fields: map[string]fieldDTO{}}
	for f, v := range cur {
		resp.Fields[string(f)] = fieldDTO{
			Value:          v.Value,
			Confidence:     float64(v.Confidence),
			Provider:       v.Prov.Provider,
			CostCredits:    int64(v.Prov.CostCredits),
			IdempotencyKey: v.Prov.IdempotencyKey,
			ObservedAt:     v.Prov.ObservedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
	}
	return resp
}

// statusCodeForStatus maps a job status to the HTTP code used when returning it.
func statusCodeForStatus(s job.Status) int {
	switch s {
	case job.StatusSucceeded, job.StatusFailed:
		return 200
	default:
		return 202
	}
}
