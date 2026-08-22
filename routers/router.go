// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package routers

import (
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"

	"github.com/hanzoai/visor/controllers"
	"github.com/hanzoai/visor/pkg/gone"
)

// h adapts a controller method to a zip.Handler: it binds a fresh controller to
// the request context and invokes the method. This is the ONE bridge from the
// method-set controllers to the framework — every route goes through it.
func h(fn func(*controllers.ApiController)) zip.Handler {
	return func(c *zip.Ctx) error {
		fn(controllers.New(c))
		return nil
	}
}

// corsPolicy is what a browser is told it may send, and the header list is the
// load-bearing part of it. Visor authenticates with a Bearer token, so leaving
// Authorization out of the preflight answer does not merely reject the header —
// the browser refuses to send the request at all and reports it as a CORS
// failure, which reads like a network fault and hides that the API was reachable
// the whole time. The list came from upstream without it, and that went
// unnoticed for as long as every caller was another server.
var corsPolicy = middleware.CORSConfig{
	AllowOrigins:  []string{"*"},
	AllowMethods:  []string{"GET", "POST", "DELETE", "PUT", "PATCH", "OPTIONS"},
	AllowHeaders:  []string{"Origin", "X-Requested-With", "Content-Type", "Accept", "Authorization"},
	ExposeHeaders: []string{"Content-Length"},
	AllowCreds:    true,
}

// Route mounts the whole visor HTTP surface on app: the ambient filter chain
// (recover, CORS, static, tenant, authz, record) followed by the /v1 API. The
// filter order is load-bearing — static short-circuits before the authz seam, so
// an asset is never gated; every /v1 route registered after the chain is
// tenant-scoped, authorized and audited.
func Route(app *zip.App) {
	app.Use(middleware.Recover())
	app.Use(middleware.CORS(corsPolicy))

	// Health is registered AHEAD of the rest of the chain, and the position is
	// the whole design. A probe carries no credentials, so ApiFilter would put it
	// to the policy engine as "anonymous" — which means a mistake in an authz
	// policy stops answering probes, kubelet reads that as every pod being sick,
	// and an authorization bug becomes a total outage. RecordMessage is the other
	// half: it writes an audit row per request, and two probes every ten seconds
	// is seventeen thousand rows a day describing nothing.
	//
	// Static is passed too, which is what the old /api/health could not do: any
	// path outside /v1/ falls to the SPA fallback and comes back 200, so the
	// probe measured the file server rather than the service.
	registerHealth(app)

	app.Use(zip.H(TransparentStatic))
	app.Use(zip.H(TenantContextFilter))
	app.Use(zip.H(ApiFilter))
	app.Use(zip.H(RecordMessage))

	registerAPI(app)
}

// registerHealth declares the health op. It is a TYPED zip op — the In and Out
// are named types, so it projects into the OpenAPI document, the MCP tool
// surface, the CLI and the by-name call plane, and an in-process caller can
// reach it through zip.Here without a wire. An untyped route would appear in
// none of those.
func registerHealth(app *zip.App) {
	zip.Get[Ping, Health](app, "/v1/health", health,
		zip.WithSummary("Report whether visor can reach its store"),
		zip.WithOperationID("health"),
		zip.WithTags("Health"),
	)
}

// registerAgent declares a machine's AGENT — the record that it runs the
// @hanzo/bot runtime for one cloud Agent (controllers/agent_binding.go). Four
// TYPED ops, so this noun is in the registry every projection reads (OpenAPI,
// MCP, CLI, the by-name call plane) rather than only on the wire.
//
// ONE noun, ONE address, and the METHOD carries the verb. PUT rather than POST
// because binding is idempotent: re-binding the same agent to the same machine
// is the state the caller asked for, not a second binding.
//
// It is called next to the other /v1/machines routes because that is where the
// noun lives, and NOT because the position decides anything: fiber prefers a
// static segment over a `:param` at the same position however the two were
// registered, so /v1/machines/agents beats /v1/machines/:id on specificity.
// Measured, not assumed — moving this call below the :id routes leaves the
// literal still winning. What is worth pinning is the OUTCOME rather than an
// ordering rule that turns out not to be one, so TestAgentsIsNotAMachineId
// asserts which handler answers.
func registerAgent(app *zip.App) {
	zip.Get(app, "/v1/machines/agents", controllers.ListAgents,
		zip.WithSummary("List the caller org's agent bindings"),
		zip.WithOperationID("listAgents"),
		zip.WithTags("AgentBinding"),
	)
	zip.Put(app, "/v1/machines/:id/agent", controllers.BindAgent,
		zip.WithSummary("Bind a cloud Agent to a machine"),
		zip.WithOperationID("bindAgent"),
		zip.WithTags("AgentBinding"),
	)
	zip.Get(app, "/v1/machines/:id/agent", controllers.GetAgent,
		zip.WithSummary("Read a machine's agent binding"),
		zip.WithOperationID("getAgent"),
		zip.WithTags("AgentBinding"),
	)
	zip.Delete(app, "/v1/machines/:id/agent", controllers.UnbindAgent,
		zip.WithSummary("Unbind a machine's agent"),
		zip.WithOperationID("unbindAgent"),
		zip.WithTags("AgentBinding"),
	)
}

// registerPlans declares the PLAN catalog — the resale tiers a brand sells
// (controllers/plan.go). Five TYPED ops at ONE address, with the METHOD
// carrying the verb, where there were five addresses each naming an operation.
//
// PUT rather than POST for the replace, because it writes every column: the
// plan that comes back is the plan that was sent.
//
// The five verb addresses are retired in the same call, so a client holding one
// is told 410 and where the catalog moved to rather than 404 and nothing.
func registerPlans(app *zip.App) {
	zip.Get(app, "/v1/plans", controllers.ListPlans,
		zip.WithSummary("List a catalog's active plans"),
		zip.WithOperationID("listPlans"),
		zip.WithTags("Plan"),
	)
	zip.Post(app, "/v1/plans", controllers.CreatePlan,
		zip.WithSummary("Add a plan to a catalog"),
		zip.WithOperationID("createPlan"),
		zip.WithTags("Plan"),
	)
	zip.Get(app, "/v1/plans/:name", controllers.GetPlan,
		zip.WithSummary("Read one plan"),
		zip.WithOperationID("getPlan"),
		zip.WithTags("Plan"),
	)
	zip.Put(app, "/v1/plans/:name", controllers.ReplacePlan,
		zip.WithSummary("Replace one plan"),
		zip.WithOperationID("replacePlan"),
		zip.WithTags("Plan"),
	)
	zip.Delete(app, "/v1/plans/:name", controllers.DeletePlan,
		zip.WithSummary("Remove one plan"),
		zip.WithOperationID("deletePlan"),
		zip.WithTags("Plan"),
	)
	gone.Serve(app, gonePlans)
}

// registerWhitelabel declares the WHITELABEL of a hostname — the branding visor
// serves under it (controllers/whitelabel.go). ONE typed op, reading the
// hostname it is asked about as a declared input rather than off the request,
// which is what lets an in-process caller ask about a host it is not serving.
func registerWhitelabel(app *zip.App) {
	zip.Get(app, "/v1/whitelabel", controllers.GetWhitelabel,
		zip.WithSummary("Read the branding a hostname serves"),
		zip.WithOperationID("whitelabel"),
		zip.WithTags("Whitelabel"),
	)
	gone.Serve(app, goneWhitelabel)
}

// registerAPI registers the /v1 surface, one verb per line. The table is pinned
// by router_contract_test.go: a route added here without a contract line fails,
// and so does a contract line with no route.
func registerAPI(app *zip.App) {
	app.Post("/v1/signin", h((*controllers.ApiController).Signin))
	app.Post("/v1/signout", h((*controllers.ApiController).Signout))
	app.Get("/v1/get-account", h((*controllers.ApiController).GetAccount))

	app.Get("/v1/get-records", h((*controllers.ApiController).GetRecords))
	app.Get("/v1/get-record", h((*controllers.ApiController).GetRecord))
	app.Post("/v1/update-record", h((*controllers.ApiController).UpdateRecord))
	app.Post("/v1/add-record", h((*controllers.ApiController).AddRecord))
	app.Post("/v1/delete-record", h((*controllers.ApiController).DeleteRecord))

	app.Post("/v1/commit-record", h((*controllers.ApiController).CommitRecord))
	app.Get("/v1/query-record", h((*controllers.ApiController).QueryRecord))

	app.Get("/v1/get-assets", h((*controllers.ApiController).GetAssets))
	app.Get("/v1/get-asset", h((*controllers.ApiController).GetAsset))
	app.Post("/v1/update-asset", h((*controllers.ApiController).UpdateAsset))
	app.Post("/v1/add-asset", h((*controllers.ApiController).AddAsset))
	app.Post("/v1/delete-asset", h((*controllers.ApiController).DeleteAsset))

	app.Get("/v1/get-providers", h((*controllers.ApiController).GetProviders))
	app.Get("/v1/get-provider", h((*controllers.ApiController).GetProvider))
	app.Post("/v1/update-provider", h((*controllers.ApiController).UpdateProvider))
	app.Post("/v1/add-provider", h((*controllers.ApiController).AddProvider))
	app.Post("/v1/delete-provider", h((*controllers.ApiController).DeleteProvider))

	app.Get("/v1/get-machines", h((*controllers.ApiController).GetMachines))
	app.Get("/v1/get-machine", h((*controllers.ApiController).GetMachine))
	app.Post("/v1/update-machine", h((*controllers.ApiController).UpdateMachine))
	app.Post("/v1/add-machine", h((*controllers.ApiController).AddMachine))
	app.Post("/v1/delete-machine", h((*controllers.ApiController).DeleteMachine))
	app.Post("/v1/launch-machine", h((*controllers.ApiController).LaunchMachine))

	// Canonical /v1 resell compute surface — cached DigitalOcean catalog and
	// per-org machines over the configured cloud account (controllers/compute.go).
	app.Get("/v1/regions", h((*controllers.ApiController).GetComputeRegions))
	app.Get("/v1/sizes", h((*controllers.ApiController).GetComputeSizes))
	app.Get("/v1/gpus", h((*controllers.ApiController).GetComputeGPUs))
	app.Get("/v1/machines", h((*controllers.ApiController).ListComputeMachines))
	app.Post("/v1/machines/launch", h((*controllers.ApiController).LaunchComputeMachine))
	registerAgent(app)
	app.Get("/v1/machines/:id", h((*controllers.ApiController).GetComputeMachine))
	app.Delete("/v1/machines/:id", h((*controllers.ApiController).DeleteComputeMachine))
	// Unified /v1/k8s noun — the ONE Kubernetes surface: DOKS cluster lifecycle
	// (list / detail+nodes / create / delete) plus the worker NODES on the fleet.
	app.Get("/v1/k8s/providers", h((*controllers.ApiController).ListComputeKubernetesProviders))
	app.Get("/v1/k8s/clusters", h((*controllers.ApiController).ListComputeKubernetesClusters))
	app.Post("/v1/k8s/clusters", h((*controllers.ApiController).CreateComputeKubernetesCluster))
	app.Get("/v1/k8s/clusters/:id", h((*controllers.ApiController).GetComputeKubernetesCluster))
	app.Delete("/v1/k8s/clusters/:id", h((*controllers.ApiController).DeleteComputeKubernetesCluster))
	// The worker NODES are a TYPED op. Every other line in this table registers a
	// handler and nothing else: the route exists on the wire and in none of the
	// projections, so the OpenAPI document, the MCP tool list, the CLI and the
	// generated SDKs do not know it is there. This one is declared with its In and
	// Out, so it appears in all of them — and cloud, which folds these nodes into
	// the fleet, can be generated against it instead of hand-written to match.
	zip.Get[controllers.Scope, controllers.Nodes](app, "/v1/k8s/nodes", controllers.ListNodes,
		zip.WithSummary("List the org's managed-Kubernetes worker nodes as machines"),
		zip.WithOperationID("nodes"),
		zip.WithTags("Compute"),
	)
	app.Get("/v1/images", h((*controllers.ApiController).ListImages))
	app.Post("/v1/images", h((*controllers.ApiController).CreateImage))

	app.Get("/v1/get-sessions", h((*controllers.ApiController).GetSessions))
	app.Get("/v1/get-session", h((*controllers.ApiController).GetConnSession))
	app.Post("/v1/update-session", h((*controllers.ApiController).UpdateSession))
	app.Post("/v1/add-session", h((*controllers.ApiController).AddSession))
	app.Post("/v1/delete-session", h((*controllers.ApiController).DeleteSession))
	app.Post("/v1/start-session", h((*controllers.ApiController).StartSession))
	app.Post("/v1/stop-session", h((*controllers.ApiController).StopSession))

	app.Post("/v1/add-asset-tunnel", h((*controllers.ApiController).AddAssetTunnel))
	app.Get("/v1/get-asset-tunnel", h((*controllers.ApiController).GetAssetTunnel))

	app.Get("/v1/get-node-pools", h((*controllers.ApiController).GetNodePools))
	app.Get("/v1/get-node-pool", h((*controllers.ApiController).GetNodePool))
	app.Post("/v1/create-node-pool", h((*controllers.ApiController).CreateNodePool))
	app.Post("/v1/update-node-pool", h((*controllers.ApiController).UpdateNodePool))
	app.Post("/v1/delete-node-pool", h((*controllers.ApiController).DeleteNodePool))
	app.Post("/v1/scale-node-pool", h((*controllers.ApiController).ScaleNodePool))

	registerPlans(app)
	registerWhitelabel(app)

	app.Get("/v1/get-volumes", h((*controllers.ApiController).GetVolumes))
	app.Get("/v1/get-volume", h((*controllers.ApiController).GetVolume))
	app.Post("/v1/create-volume", h((*controllers.ApiController).CreateVolume))
	app.Post("/v1/delete-volume", h((*controllers.ApiController).DeleteVolume))
	app.Post("/v1/attach-volume", h((*controllers.ApiController).AttachVolume))
	app.Post("/v1/detach-volume", h((*controllers.ApiController).DetachVolume))
	app.Post("/v1/resize-volume", h((*controllers.ApiController).ResizeVolume))
}
