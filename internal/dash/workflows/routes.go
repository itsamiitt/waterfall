package workflows

import (
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/router"
)

// Deps bundles the collaborators Routes needs. Service is the shared configver lifecycle engine
// (constructed in cmd/dashboardd with the waterfall_workflow Validator registered); Providers backs
// the dry-run simulator's live capabilities.
type Deps struct {
	Service      *configver.Service
	Providers    configver.ProviderSource
	Auth         configver.Authenticator
	DryRunClient *http.Client  // optional; tests inject a fail-on-request transport (zero-egress)
	Scorer       router.Scorer // optional bandit posteriors
	Logger       *slog.Logger
}

// Routes mounts the Waterfall Configuration endpoints (doc 04 §2.7) at /v1/admin/workflows. The
// collection GET serves the denormalized workflow_index (the Waterfall list view); the per-scope
// lifecycle delegates to configver.Mount. This package supplies only the waterfall_workflow KindSpec
// + dry-run simulator.
func Routes(mux *http.ServeMux, d Deps) {
	spec := configver.KindSpec{
		Kind:        configver.KindWaterfallWorkflow,
		BasePath:    "/v1/admin/workflows",
		RBACAction:  rbac.WorkflowsPublish,
		DryRun:      NewDryRunner(d.Providers, d.DryRunClient, d.Scorer),
		IndexAsList: true, // GET /v1/admin/workflows serves the workflow_index
	}
	hd := configver.HTTPDeps{Service: d.Service, Auth: d.Auth, Logger: d.Logger}
	configver.Mount(mux, spec, hd)
}
