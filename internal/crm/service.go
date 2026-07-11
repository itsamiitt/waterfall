package crm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/enrichment/waterfall/internal/tenant"
)

// Service performs idempotent CRM pushes (G2, ADR-0030): it CLAIMS the push in the ledger and only when
// the (tenant, key) is NEW does it execute the egress push; a redelivery is a deduplicated no-op. On push
// failure the claim is released so the push is retryable — the ledger therefore records only pushes that
// reached the CRM. Tenant scoping is enforced by the Store's RLS (the claim, the connection lookup, and the
// ledger all run in the caller's tenant).
type Service struct {
	Store  *Store
	Pusher *Pusher
}

// NewService wires the ledger store and the egress pusher.
func NewService(store *Store, pusher *Pusher) *Service { return &Service{Store: store, Pusher: pusher} }

// Push idempotently writes one record to a connection's CRM through the egress boundary. Returns
// pushed=false when the push was a deduplicated no-op (a prior successful push claimed the same key).
func (s *Service) Push(ctx context.Context, connectionID, record, dossierVersion string, fieldMapVersion int, endpoint string, body json.RawMessage) (bool, error) {
	ten, err := tenant.TenantID(ctx)
	if err != nil {
		return false, err
	}
	key := PushKey(ten, connectionID, record, fieldMapVersion, dossierVersion)

	claimed, err := s.Store.RecordPush(ctx, PushRecord{
		ConnectionID:    connectionID,
		IdemKey:         key,
		Record:          record,
		FieldMapVersion: fieldMapVersion,
		DossierVersion:  dossierVersion,
	})
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil // idempotent no-op: a prior push already claimed this key
	}

	conn, ok, err := s.Store.GetConnection(ctx, connectionID)
	if err != nil {
		return false, err
	}
	if !ok {
		_ = s.Store.DeletePush(ctx, key)
		return false, fmt.Errorf("crm: unknown connection %q", connectionID)
	}

	if err := s.Pusher.Push(ctx, conn.Provider, PushInput{Endpoint: endpoint, SecretRef: conn.SecretRef, Body: body}); err != nil {
		_ = s.Store.DeletePush(ctx, key) // release the claim so a retry can re-attempt
		return false, err
	}
	return true, nil
}
