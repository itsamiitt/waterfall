package routing

import (
	"log/slog"
	"net/http"

	"github.com/enrichment/waterfall/internal/dash/approvals"
	"github.com/enrichment/waterfall/internal/dash/configver"
	"github.com/enrichment/waterfall/internal/dash/rbac"
	"github.com/enrichment/waterfall/internal/router"
)

// Deps bundles the collaborators Routes needs. Service is the shared configver lifecycle engine
// (constructed in cmd/dashboardd with the routing_policy Validator registered); Providers backs the
// dry-run simulator's live capabilities.
type Deps struct {
	Service      *configver.Service
	Providers    configver.ProviderSource
	Auth         configver.Authenticator
	Gate         approvals.Gate // optional; nil => publish/rollback run inline (no approval gate)
	DryRunClient *http.Client   // optional; tests inject a fail-on-request transport (zero-egress)
	Scorer       router.Scorer  // optional bandit posteriors
	Logger       *slog.Logger
}

// Routes mounts the Request Routing Center endpoints (doc 04 §2.7) at /v1/admin/routing plus the
// shared GET /v1/admin/config/epochs poll target. It delegates the full draft->publish lifecycle to
// configver.Mount; this package supplies only the routing_policy KindSpec + dry-run simulator.
func Routes(mux *http.ServeMux, d Deps) {
	spec := configver.KindSpec{
		Kind:       configver.KindRoutingPolicy,
		BasePath:   "/v1/admin/routing",
		RBACAction: rbac.RoutingPublish,
		DryRun:     NewDryRunner(d.Providers, d.DryRunClient, d.Scorer),
		Gate:       d.Gate,
		GateAction: approvals.ActionRoutingPublish,
	}
	hd := configver.HTTPDeps{Service: d.Service, Auth: d.Auth, Logger: d.Logger}
	configver.Mount(mux, spec, hd)
	configver.MountEpochs(mux, hd)
}
