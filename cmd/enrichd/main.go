// Command enrichd is a runnable demonstration of the Waterfall Enrichment Engine
// vertical slice: it wires the Adaptive Router, the Execution Engine (G1–G5 gates), and
// the in-memory Store, then enriches one Person record through two mock Providers and
// prints the outcome with full provenance and cost accounting.
//
// It uses in-memory fake providers so it runs offline; the same Engine drives real
// HTTPAdapter providers unchanged (see internal/provider/httpadapter.go).
package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/enrichment/waterfall/internal/domain"
	"github.com/enrichment/waterfall/internal/engine"
	"github.com/enrichment/waterfall/internal/provider"
	"github.com/enrichment/waterfall/internal/provider/adapters"
	"github.com/enrichment/waterfall/internal/provider/providertest"
	"github.com/enrichment/waterfall/internal/router"
	"github.com/enrichment/waterfall/internal/store"
	"github.com/enrichment/waterfall/internal/tenant"
)

func main() {
	// Two mock providers with different cost/confidence per Field. In production these are
	// HTTPAdapters against real vendor APIs; the wiring is identical.
	cheapEmail := providertest.New("vendor-cheap", "jane.doe@acme.com", 0.72, 2,
		domain.FieldWorkEmail)
	premiumEmail := providertest.New("vendor-premium", "jane.doe@acme.com", 0.80, 6,
		domain.FieldWorkEmail)
	phone := providertest.New("vendor-phone", "+1-555-0100", 0.88, 5,
		domain.FieldMobilePhone)

	st := store.NewMemory()
	eng := engine.New(st, []provider.Adapter{cheapEmail, premiumEmail, phone})
	plan := router.New(cheapEmail, premiumEmail, phone)

	// The request: enrich a Person to 0.9 confidence, spending at most 15 credits.
	req := domain.EnrichmentRequest{
		JobID: "job-demo",
		Subject: domain.Subject{
			ID:    "person-42",
			Known: map[domain.Field]string{domain.FieldCompanyDomain: "acme.com", domain.FieldJobTitle: "VP Sales"},
		},
		Want:             []domain.Field{domain.FieldWorkEmail, domain.FieldMobilePhone},
		ConfidenceTarget: 0.90,
		CostCeiling:      15,
		ConfigVersion:    "demo-v1",
	}

	// G1: bind the authenticated principal. tenant_id flows ONLY from here.
	ctx := tenant.WithPrincipal(context.Background(), tenant.Principal{TenantID: "tenant-acme", UserID: "user-1"})

	out, err := eng.Run(ctx, req, plan.Plan(req))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("Waterfall Enrichment Engine — vertical slice demo")
	fmt.Println("=================================================")
	fmt.Printf("tenant=%s job=%s subject=%s ceiling=%d target=%.2f\n\n",
		"tenant-acme", req.JobID, req.Subject.ID, req.CostCeiling, req.ConfidenceTarget)

	fields := make([]domain.Field, 0, len(req.Want))
	fields = append(fields, req.Want...)
	sort.Slice(fields, func(i, j int) bool { return fields[i] < fields[j] })

	for _, f := range fields {
		fv, ok := out.Filled[f]
		if !ok {
			fmt.Printf("  %-14s (unfilled)            stop=%s\n", f, out.Stops[f])
			continue
		}
		fmt.Printf("  %-14s = %-22s conf=%.3f  via %-16s cost=%d  stop=%s\n",
			f, fv.Value, fv.Confidence, fv.Prov.Provider, fv.Prov.CostCredits, out.Stops[f])
		fmt.Printf("       provenance: idempotency_key=%s…  observed_at=%s\n",
			fv.Prov.IdempotencyKey[:12], fv.Prov.ObservedAt.Format("2006-01-02T15:04:05Z"))
	}
	fmt.Printf("\ntotal committed: %d credits (ceiling %d)\n", out.Committed, req.CostCeiling)

	// Demonstrate G2: a replay makes no new provider calls and no new charge.
	before := cheapEmail.Calls() + premiumEmail.Calls() + phone.Calls()
	_, _ = eng.Run(ctx, req, plan.Plan(req))
	after := cheapEmail.Calls() + premiumEmail.Calls() + phone.Calls()
	fmt.Printf("replay (idempotent): provider calls before=%d after=%d (G2: no new paid calls)\n", before, after)

	// The same Engine drives the real API-first adapters from the registry unchanged. They are
	// constructed offline here (no network / no keys) purely to show the wired catalog; a live
	// run (see cmd/enrichapi) resolves keys and calls each vendor through the egress SSRF choke.
	fmt.Println("\nRegistered API-first adapters (internal/provider/adapters):")
	for _, r := range adapters.Registry() {
		fmt.Printf("  %-18s %-16s %s\n", r.Slug, r.Category, r.Status)
	}
}
