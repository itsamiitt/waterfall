// Package job models asynchronous enrichment work: a Job is one EnrichmentRequest queued
// for a worker to execute through the engine. It provides a tenant-scoped JobStore
// (gate G1), a deterministic idempotent job id (API-level idempotency, docs/07), and a
// bounded priority Queue with a worker-pool Dispatcher (docs/10 async unit; docs/11
// back-pressure).
package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/tenant"
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Priority selects the queue lane. Premium tenants are not starved by bulk jobs
// (docs/11 §4 fair-share).
type Priority int

const (
	PriorityBulk    Priority = 0
	PriorityPremium Priority = 1
)

// Job is one unit of asynchronous enrichment.
type Job struct {
	ID             string
	TenantID       string
	IdempotencyKey string           // client-supplied Idempotency-Key (API write dedupe)
	Fingerprint    string           // hash of the request params, to detect key reuse with a different body
	Principal      tenant.Principal // captured at submit; tenant_id flows from here to the worker (G1)
	Req            domain.EnrichmentRequest
	Priority       Priority

	Status    Status
	Outcome   *engine.Outcome
	Err       string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DeriveID computes a deterministic job id from the tenant and the client idempotency
// key, so resubmitting the same key returns the same job with no random state.
func DeriveID(tenantID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x1f" + idempotencyKey))
	return "job_" + hex.EncodeToString(sum[:16])
}

// Fingerprint hashes the semantically-significant request fields so the API can reject an
// Idempotency-Key reused with a different payload (409).
func Fingerprint(req domain.EnrichmentRequest) string {
	h := sha256.New()
	writePart(h, req.Subject.ID)
	for _, f := range req.Want {
		writePart(h, string(f))
	}
	writePart(h, req.ConfigVersion)
	// ceiling + target influence the result, so they are part of the identity.
	writePart(h, strconv.FormatInt(int64(req.CostCeiling), 10))
	writePart(h, strconv.FormatFloat(float64(req.ConfidenceTarget), 'f', 6, 64))
	// Known attributes in a stable order so the fingerprint is deterministic.
	keys := make([]string, 0, len(req.Subject.Known))
	for k := range req.Subject.Known {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		writePart(h, k+"="+req.Subject.Known[domain.Field(k)])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writePart(h interface{ Write([]byte) (int, error) }, s string) {
	var l [4]byte
	n := uint32(len(s))
	l[0], l[1], l[2], l[3] = byte(n), byte(n>>8), byte(n>>16), byte(n>>24)
	_, _ = h.Write(l[:])
	_, _ = h.Write([]byte(s))
}

// contextFor builds a worker context carrying the job's captured principal, so the
// engine and store run under the submitter's tenant (G1).
func (j *Job) contextFor(base context.Context) context.Context {
	return tenant.WithPrincipal(base, j.Principal)
}
