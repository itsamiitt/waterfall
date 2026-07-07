// Command providerseed projects the CODE adapter registry (internal/provider/adapters) into the
// platform provider CATALOG (migration 0005 providers table). For every Registered adapter it
// constructs the adapter, reads its verified integration descriptor (NameV / BaseURL / Auth /
// Caps), and UPSERTs one providers row under the platform tenant — so the catalog is a projection
// of the code and the runtime adapter and its catalog row can never drift (ADR-0023).
//
// New rows land op_state='disabled' (DB default): nothing serves until an operator reviews the
// row, sets compliance_review_status where the provider is DEPRIORITIZED, creates a
// "<slug>:default" key pool + keys, and enables it. Re-running refreshes ONLY the integration
// descriptor, never operator lifecycle state — so it is safe to run repeatedly after adding
// adapters. Dev/ops tooling; not a production deploy step.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/enrichment/waterfall/internal/dash/db"
	"github.com/enrichment/waterfall/internal/dash/providers"
	"github.com/enrichment/waterfall/internal/pg"
	"github.com/enrichment/waterfall/internal/provider/adapters"
)

func main() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "providerseed: POSTGRES_DSN is required")
		os.Exit(1)
	}
	pool := pg.NewPool(pg.ParseDSN(dsn), 4)
	defer pool.Close()
	store := providers.NewPGStore(db.New(pool))

	ctx := context.Background()
	created, refreshed := 0, 0
	for _, r := range adapters.Registry() {
		_, wasCreated, err := providers.Seed(ctx, store, seedInputFor(r))
		if err != nil {
			fmt.Fprintf(os.Stderr, "providerseed: %s: %v\n", r.Slug, err)
			os.Exit(1)
		}
		verb := "refreshed"
		if wasCreated {
			created++
			verb = "created"
		} else {
			refreshed++
		}
		fmt.Printf("  %-18s %-16s %s\n", r.Slug, r.Status, verb)
	}
	fmt.Printf("providerseed OK: %d providers (%d created, %d refreshed)\n",
		created+refreshed, created, refreshed)
}

// seedInputFor maps a registered adapter to a catalog SeedInput. It constructs the adapter with a
// nil client purely to read its descriptor (NameV/BaseURL/Auth/Caps) — it never performs a Fetch.
func seedInputFor(r adapters.Registered) providers.SeedInput {
	a := r.Construct("", nil) // provider.Introspectable — HTTPAdapter or AsyncHTTPAdapter
	auth := a.AuthDescriptor()
	adapterCaps := a.Capabilities()
	caps := make([]providers.Capability, 0, len(adapterCaps))
	var minCost int64
	for i, c := range adapterCaps {
		caps = append(caps, providers.Capability{
			Field:              string(c.Field),
			CostCredits:        int64(c.Cost),
			ExpectedConfidence: float64(c.ExpectedConfidence),
		})
		if i == 0 || int64(c.Cost) < minCost {
			minCost = int64(c.Cost)
		}
	}
	return providers.SeedInput{
		ID:              a.Name(),
		DisplayName:     displayName(r.Slug),
		Category:        r.Category,
		Status:          r.Status,
		BaseURL:         a.Base(),
		AuthScheme:      string(auth.Scheme),
		AuthHeader:      auth.HeaderName,
		AuthQueryParam:  auth.QueryParam,
		Capabilities:    caps,
		Region:          r.Regions,
		DocsURL:         r.DocsURL,
		UnitCostCredits: &minCost,
	}
}

// displayName derives a human catalog name from a slug ("twilio-lookup" -> "Twilio Lookup").
func displayName(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
