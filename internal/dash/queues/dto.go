package queues

import (
	"encoding/json"
	"time"
)

// Wire DTOs for module 8. Timestamps render as RFC3339 (or null); no payload/secret material.

type queueDTO struct {
	Name            string `json:"name"`
	Kind            string `json:"kind,omitempty"`
	MaxAttempts     int    `json:"max_attempts"`
	VisibilityS     int    `json:"visibility_s"`
	Description     string `json:"description,omitempty"`
	DesiredReplicas *int64 `json:"desired_replicas,omitempty"`
	Depth           int64  `json:"depth"`
	Running         int64  `json:"running"`
	Scheduled       int64  `json:"scheduled"`
	Delayed         int64  `json:"delayed"`
	Retry           int64  `json:"retry"`
	Failed          int64  `json:"failed"`
	Dead            int64  `json:"dead"`
	Enq             int64  `json:"enq"`
	Deq             int64  `json:"deq"`
	OldestAgeS      int64  `json:"oldest_age_s"`
	SampleAt        any    `json:"sample_at"`
}

func toQueueDTO(q QueueSummary) queueDTO {
	return queueDTO{
		Name: q.Name, Kind: q.Kind, MaxAttempts: q.MaxAttempts, VisibilityS: q.VisibilityS,
		Description: q.Description, DesiredReplicas: q.DesiredReplicas,
		Depth: q.Depth, Running: q.Running, Scheduled: q.Scheduled, Delayed: q.Delayed,
		Retry: q.Retry, Failed: q.Failed, Dead: q.Dead, Enq: q.Enq, Deq: q.Deq,
		OldestAgeS: q.OldestAgeS, SampleAt: tsOrNull(q.SampleAt),
	}
}

type bucketDTO struct {
	BucketStart string `json:"bucket_start"`
	Depth       int64  `json:"depth"`
	Running     int64  `json:"running"`
	Scheduled   int64  `json:"scheduled"`
	Delayed     int64  `json:"delayed"`
	Retry       int64  `json:"retry"`
	Failed      int64  `json:"failed"`
	Dead        int64  `json:"dead"`
	Enq         int64  `json:"enq"`
	Deq         int64  `json:"deq"`
	OldestAgeS  int64  `json:"oldest_age_s"`
}

func toBucketDTO(b StatsBucket) bucketDTO {
	return bucketDTO{
		BucketStart: b.BucketStart.UTC().Format(time.RFC3339), Depth: b.Depth, Running: b.Running,
		Scheduled: b.Scheduled, Delayed: b.Delayed, Retry: b.Retry, Failed: b.Failed, Dead: b.Dead,
		Enq: b.Enq, Deq: b.Deq, OldestAgeS: b.OldestAgeS,
	}
}

type jobDTO struct {
	JobID     string `json:"job_id"`
	State     string `json:"state"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	Dead      bool   `json:"dead"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toJobDTO(j JobRow) jobDTO {
	return jobDTO{
		JobID: j.JobID, State: string(j.State), Status: j.Status, Attempts: j.Attempts, Dead: j.Dead,
		CreatedAt: j.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: j.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type deadDTO struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

func toDeadDTO(d DeadLetterRow) deadDTO {
	return deadDTO{
		JobID: d.JobID, Status: d.Status, Attempts: d.Attempts, LastError: d.LastError,
		UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339), CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type jobDetailDTO struct {
	JobID      string   `json:"job_id"`
	Status     string   `json:"status"`
	State      string   `json:"state"`
	Attempts   int      `json:"attempts"`
	Dead       bool     `json:"dead"`
	Pending    bool     `json:"pending"`
	LastError  string   `json:"last_error,omitempty"`
	SubjectID  string   `json:"subject_id,omitempty"`
	WantFields []string `json:"want_fields,omitempty"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	ClaimedAt  any      `json:"claimed_at"`
}

func toJobDetailDTO(d JobDetail) jobDetailDTO {
	out := jobDetailDTO{
		JobID: d.JobID, Status: d.Status, State: string(d.State), Attempts: d.Attempts,
		Dead: d.Dead, Pending: d.Pending, LastError: d.LastError, SubjectID: d.SubjectID,
		WantFields: d.WantFields, CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339), ClaimedAt: nil,
	}
	if d.ClaimedAt != nil {
		out.ClaimedAt = d.ClaimedAt.UTC().Format(time.RFC3339)
	}
	return out
}

type bulkJobDTO struct {
	JobID              string          `json:"job_id"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	Total              int             `json:"total"`
	Succeeded          int             `json:"succeeded"`
	Failed             int             `json:"failed"`
	MatchedAtExecution int             `json:"matched_at_execution"`
	Results            json.RawMessage `json:"results"`
	CreatedAt          string          `json:"created_at"`
	StartedAt          any             `json:"started_at"`
	FinishedAt         any             `json:"finished_at"`
}

func toBulkJobDTO(j BulkJob) bulkJobDTO {
	out := bulkJobDTO{
		JobID: j.ID, Kind: j.Kind, Status: j.Status, Total: j.Total, Succeeded: j.Succeeded,
		Failed: j.Failed, MatchedAtExecution: j.MatchedAtExecution,
		CreatedAt: j.CreatedAt.UTC().Format(time.RFC3339), StartedAt: nil, FinishedAt: nil,
	}
	if len(j.Results) > 0 {
		out.Results = json.RawMessage(j.Results)
	} else {
		out.Results = json.RawMessage("[]")
	}
	if j.StartedAt != nil {
		out.StartedAt = j.StartedAt.UTC().Format(time.RFC3339)
	}
	if j.FinishedAt != nil {
		out.FinishedAt = j.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func tsOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
