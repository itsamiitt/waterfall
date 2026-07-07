package providers

import (
	"context"
	"errors"
)

// This file is the catalog-seed seam used by cmd/providerseed to project the CODE adapter
// registry (internal/provider/adapters) into the platform providers catalog (migration 0005),
// so a runtime adapter and its catalog row can never drift (ADR-0023). It lives in-package so the
// unexported colVal/colsBuilder encoding stays encapsulated; the input is a neutral SeedInput that
// carries no dependency on the engine adapter types.

// SeedInput is a neutral catalog seed row for one provider. It carries only the integration
// DESCRIPTOR (base URL + auth scheme/header + capabilities) plus the ADR-0009 inclusion status —
// never secrets, never runtime scores. cmd/providerseed builds it from a registered adapter.
type SeedInput struct {
	ID              string
	DisplayName     string
	Category        string
	Status          string // ADR-0009: ACTIVE-CANDIDATE | DEPRIORITIZED (EXCLUDED is never seeded)
	BaseURL         string
	AuthScheme      string
	AuthHeader      string
	AuthQueryParam  string
	Capabilities    []Capability
	Region          []string
	DocsURL         string
	UnitCostCredits *int64
}

// Seed idempotently UPSERTs one catalog row from in, running under db.Store.PlatformTx via store.
// A new slug is INSERTed carrying status + the DB defaults op_state='disabled' /
// visibility='tenant_readable' — so a freshly seeded provider is UNAVAILABLE until an operator
// reviews it, (for DEPRIORITIZED) approves compliance, adds a "<slug>:default" key pool + keys, and
// enables it. An existing slug has ONLY its integration descriptor refreshed; status, op_state,
// compliance_review_status and every runtime column are left intact so re-seeding never clobbers
// an operator's lifecycle decisions. created reports whether a row was inserted vs. refreshed.
func Seed(ctx context.Context, store Store, in SeedInput) (p Provider, created bool, err error) {
	if in.ID == "" {
		return Provider{}, false, errors.New("providers: seed input missing id")
	}
	if !validStatuses[in.Status] {
		return Provider{}, false, ErrValidation
	}
	desc := in.descriptorCols()
	create := make([]colVal, 0, len(desc)+2)
	create = append(create, colVal{name: "id", val: in.ID}, colVal{name: "status", val: in.Status})
	create = append(create, desc...)

	p, err = store.Insert(ctx, create)
	if err == nil {
		return p, true, nil
	}
	if !errors.Is(err, ErrConflict) {
		return Provider{}, false, err
	}
	// Existing row: refresh the integration descriptor only (status/op_state/compliance untouched).
	p, err = store.Update(ctx, in.ID, desc)
	return p, false, err
}

// descriptorCols builds the config-only column set shared by create + update. It deliberately
// omits id, status, op_state, compliance_review_status and all runtime columns.
func (in SeedInput) descriptorCols() []colVal {
	var b colsBuilder
	b.strForce("display_name", in.DisplayName)
	b.str("category", in.Category)
	b.str("base_url", in.BaseURL)
	b.str("auth_scheme", in.AuthScheme)
	b.str("auth_header", in.AuthHeader)
	b.str("auth_query_param", in.AuthQueryParam)
	if len(in.Capabilities) > 0 {
		b.caps("capabilities", in.Capabilities)
	}
	if len(in.Region) > 0 {
		b.arr("region", in.Region)
	}
	b.str("docs_url", in.DocsURL)
	b.i64("unit_cost_credits", in.UnitCostCredits)
	return b.cv
}
