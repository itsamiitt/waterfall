package keys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/enrichment/waterfall/internal/dash/db"
)

// Key Pool management (doc 04 §2.4). A pool's selector is provider_id||':'||name, matching
// provider.AuthDescriptor.KeyPoolSelector. Strategy + params are validated synchronously against
// the closed 12-strategy catalog before any write (doc 07 §8.1). NOTE (OI-KEYS-4): the doc 07 §10
// key_pool config-epoch bump on strategy/member/status writes is deferred to P3, when the
// configver package that owns BumpEpoch(ctx, "key_pool") lands; P1 persists the change durably
// and audits it, which is what the acceptance gate requires.

// CreatePool inserts a key_pools row.
func (svc *Service) CreatePool(ctx context.Context, providerID, name, strategy, strategyParams, ownerTenant string) (Pool, error) {
	if name == "" {
		return Pool{}, fmt.Errorf("%w: pool name is required", ErrValidation)
	}
	if !validStrategies[strategy] {
		return Pool{}, ErrInvalidStrategy
	}
	if err := validParams(strategyParams); err != nil {
		return Pool{}, err
	}
	ok, err := svc.store.providerExists(ctx, providerID)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrProviderNotFound
	}
	p := Pool{
		ID: newID(), ProviderID: providerID, Name: name, Strategy: strategy,
		StrategyParams: strategyParams, OwnerTenantID: ownerTenant, Status: "active",
	}
	if err := svc.store.insertPool(ctx, p); err != nil {
		return Pool{}, err
	}
	fresh, _, _ := svc.store.getPool(ctx, p.ID)
	svc.appendAudit(ctx, "key_pool_create", "key_pools", p.ID, nil, poolSnapshot(fresh))
	return fresh, nil
}

// GetPool returns a pool + member count.
func (svc *Service) GetPool(ctx context.Context, id string) (Pool, error) {
	p, ok, err := svc.store.getPool(ctx, id)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	return p, nil
}

// ListPools returns a bounded page of pools.
func (svc *Service) ListPools(ctx context.Context, providerID, strategy, ownerTenant string, cur db.Cursor, limit int) ([]Pool, db.Cursor, error) {
	return svc.store.listPools(ctx, providerID, strategy, ownerTenant, cur, limit)
}

// PatchPool renames a pool / updates its status or params (never its strategy — that has a
// dedicated PUT so the epoch-bump concern lives in one place).
func (svc *Service) PatchPool(ctx context.Context, id string, name, status, strategyParams *string) (Pool, error) {
	before, ok, err := svc.store.getPool(ctx, id)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	if strategyParams != nil {
		if err := validParams(*strategyParams); err != nil {
			return Pool{}, err
		}
	}
	ok, err = svc.store.updatePool(ctx, id, name, status, strategyParams)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	after, _, _ := svc.store.getPool(ctx, id)
	svc.appendAudit(ctx, "key_pool_update", "key_pools", id, poolSnapshot(before), poolSnapshot(after))
	return after, nil
}

// DeletePool removes a pool and its membership rows (keys are unaffected).
func (svc *Service) DeletePool(ctx context.Context, id string) error {
	before, ok, err := svc.store.getPool(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPoolNotFound
	}
	ok, err = svc.store.deletePool(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrPoolNotFound
	}
	svc.appendAudit(ctx, "key_pool_delete", "key_pools", id, poolSnapshot(before), nil)
	return nil
}

// PutMembers replaces a pool's full member key set (doc 04 §2.4 full-replacement PUT). Unknown
// key ids are a 422.
func (svc *Service) PutMembers(ctx context.Context, id string, keyIDs []string) (Pool, error) {
	missing, ok, err := svc.store.replaceMembers(ctx, id, keyIDs)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	if len(missing) > 0 {
		return Pool{}, fmt.Errorf("%w: unknown key ids %v", ErrValidation, missing)
	}
	after, _, _ := svc.store.getPool(ctx, id)
	svc.appendAudit(ctx, "key_pool_members", "key_pools", id, nil, map[string]string{
		"member_count": fmt.Sprint(after.MemberCount),
	})
	return after, nil
}

// PutStrategy sets a pool's selection strategy + params (validated against the 12-strategy
// catalog).
func (svc *Service) PutStrategy(ctx context.Context, id, strategy, strategyParams string) (Pool, error) {
	if !validStrategies[strategy] {
		return Pool{}, ErrInvalidStrategy
	}
	if err := validParams(strategyParams); err != nil {
		return Pool{}, err
	}
	before, ok, err := svc.store.getPool(ctx, id)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	ok, err = svc.store.setPoolStrategy(ctx, id, strategy, strategyParams)
	if err != nil {
		return Pool{}, err
	}
	if !ok {
		return Pool{}, ErrPoolNotFound
	}
	after, _, _ := svc.store.getPool(ctx, id)
	svc.appendAudit(ctx, "key_pool_strategy", "key_pools", id, poolSnapshot(before), poolSnapshot(after))
	return after, nil
}

// PoolMembers returns the member key ids of a pool.
func (svc *Service) PoolMembers(ctx context.Context, id string) ([]string, error) {
	if _, ok, err := svc.store.getPool(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrPoolNotFound
	}
	return svc.store.poolMemberIDs(ctx, id)
}

// validParams rejects a non-empty strategy_params that is not a JSON object.
func validParams(params string) error {
	if params == "" {
		return nil
	}
	if !json.Valid([]byte(params)) {
		return fmt.Errorf("%w: strategy_params is not valid JSON", ErrValidation)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(params), &obj); err != nil {
		return fmt.Errorf("%w: strategy_params must be a JSON object", ErrValidation)
	}
	return nil
}

func poolSnapshot(p Pool) map[string]string {
	return map[string]string{
		"id": p.ID, "provider_id": p.ProviderID, "name": p.Name,
		"selector": p.Selector(), "strategy": p.Strategy, "status": p.Status,
	}
}

// --- bulk operations (POST /keys/bulk, GET /bulk-jobs/{id}) ---

// BulkInput is the bulk-op request (doc 04 §2.4/§4.2). Exactly one of IDs / Filter is set.
type BulkInput struct {
	IDs     []string
	Filter  *KeyFilter
	Op      string
	Reason  string
	Preview bool
}

// bulkOps is the closed op vocabulary supported inline in P1.
var bulkOps = map[string]bool{
	"enable": true, "disable": true, "archive": true, "delete": true,
	"health_check": true, "refresh_credits": true,
}

// BulkOp resolves the scope (ids or filter, never both — re-evaluated under RLS at execution,
// doc 04 §4.2), and for preview returns the match count only. Otherwise it executes the op inline
// (P1 model), records a BulkJob, and returns its id. op=delete is soft-archived and audited inline
// for P1; it becomes approval-gated once the P4 approvals quorum lands (OI-KEYS-1).
func (svc *Service) BulkOp(ctx context.Context, in BulkInput) (jobID string, matched int, err error) {
	if (len(in.IDs) == 0) == (in.Filter == nil) {
		return "", 0, fmt.Errorf("%w: exactly one of ids or filter is required", ErrValidation)
	}
	if !bulkOps[in.Op] {
		return "", 0, fmt.Errorf("%w: unsupported bulk op %q", ErrValidation, in.Op)
	}

	ids := in.IDs
	if in.Filter != nil {
		ids, err = svc.store.listKeyIDsByFilter(ctx, *in.Filter)
		if err != nil {
			return "", 0, err
		}
	}
	if in.Preview {
		return "", len(ids), nil
	}

	job := &BulkJob{
		ID: newID(), Kind: "key_bulk", Op: in.Op, Status: StatusImportRunning,
		Total: len(ids), MatchedAtExecution: len(ids),
	}
	for _, id := range ids {
		if e := svc.applyBulk(ctx, in.Op, id, in.Reason); e != nil {
			job.Failed++
			if len(job.Errors) < maxImportErrors {
				job.Errors = append(job.Errors, rowError{Row: 0, ID: id, Code: bulkErrCode(e), Message: bulkErrMsg(e)})
			}
		} else {
			job.Succeeded++
		}
	}
	job.Status = StatusImportSucceeded
	if job.Failed > 0 {
		job.Status = StatusImportPartial
	}
	svc.bulk.put(job)
	svc.appendAudit(ctx, "key_bulk_"+in.Op, "provider_keys", job.ID, nil, map[string]string{
		"op": in.Op, "matched": fmt.Sprint(len(ids)),
		"succeeded": fmt.Sprint(job.Succeeded), "failed": fmt.Sprint(job.Failed),
	})
	return job.ID, len(ids), nil
}

// ResolveKeyIDs resolves a bulk-op scope (explicit ids or a filter, never both) to the concrete
// Provider Key id set, so a gated bulk delete can pin the FULLY-RESOLVED id list at request time
// (approvals payload pinning). It is a read-only projection over the same RLS-scoped filter query
// BulkOp uses, so it never changes bulk behavior.
func (svc *Service) ResolveKeyIDs(ctx context.Context, in BulkInput) ([]string, error) {
	if (len(in.IDs) == 0) == (in.Filter == nil) {
		return nil, fmt.Errorf("%w: exactly one of ids or filter is required", ErrValidation)
	}
	if in.Filter != nil {
		return svc.store.listKeyIDsByFilter(ctx, *in.Filter)
	}
	return in.IDs, nil
}

func (svc *Service) applyBulk(ctx context.Context, op, id, reason string) error {
	switch op {
	case "enable":
		_, err := svc.EnableKey(ctx, id)
		return err
	case "disable":
		_, err := svc.DisableKey(ctx, id, reason)
		return err
	case "archive", "delete": // P1: delete is soft-archived inline (approval-gated in P4)
		_, err := svc.ArchiveKey(ctx, id)
		return err
	case "health_check":
		_, err := svc.HealthCheckKey(ctx, id)
		return err
	case "refresh_credits":
		_, err := svc.RefreshCredits(ctx, id)
		return err
	}
	return ErrValidation
}

// BulkStatus serves GET /bulk-jobs/{id} from the in-process registry.
func (svc *Service) BulkStatus(ctx context.Context, id string) (*BulkJob, bool) {
	return svc.bulk.get(id)
}

func bulkErrCode(err error) string {
	switch {
	case errors.Is(err, ErrInvalidTransition):
		return codeConflict
	case errors.Is(err, ErrKeyNotFound):
		return codeNotFound
	default:
		return codeInternal
	}
}

func bulkErrMsg(err error) string {
	switch {
	case errors.Is(err, ErrInvalidTransition):
		return "illegal state transition"
	case errors.Is(err, ErrKeyNotFound):
		return "key not found"
	default:
		return "internal error"
	}
}
