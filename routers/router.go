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

	"github.com/hanzoai/compute/controllers"
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
// load-bearing part of it. Compute authenticates with a Bearer token, so leaving
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

// Route mounts the whole compute HTTP surface on app: the ambient filter chain
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
	registerGone(app)

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
		zip.WithSummary("Report whether compute can reach its store"),
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
// registered, so /v1/machines/agents beats /v1/machines/:owner/:name on specificity.
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
	zip.Put(app, "/v1/machines/:owner/:name/agent", controllers.BindAgent,
		zip.WithSummary("Bind a cloud Agent to a machine"),
		zip.WithOperationID("bindAgent"),
		zip.WithTags("AgentBinding"),
	)
	zip.Get(app, "/v1/machines/:owner/:name/agent", controllers.GetAgent,
		zip.WithSummary("Read a machine's agent binding"),
		zip.WithOperationID("getAgent"),
		zip.WithTags("AgentBinding"),
	)
	zip.Delete(app, "/v1/machines/:owner/:name/agent", controllers.UnbindAgent,
		zip.WithSummary("Unbind a machine's agent"),
		zip.WithOperationID("unbindAgent"),
		zip.WithTags("AgentBinding"),
	)
}

// registerAPI registers the /v1 surface, one verb per line. The table is pinned
// by router_contract_test.go: a route added here without a contract line fails,
// and so does a contract line with no route.
func registerAPI(app *zip.App) {
	app.Post("/v1/signin", h((*controllers.ApiController).Signin))
	app.Post("/v1/signout", h((*controllers.ApiController).Signout))
	app.Get("/v1/account", h((*controllers.ApiController).GetAccount))

	app.Get("/v1/records", h((*controllers.ApiController).GetRecords))
	app.Get("/v1/records/:owner/:name", h((*controllers.ApiController).GetRecord))
	app.Put("/v1/records/:owner/:name", h((*controllers.ApiController).UpdateRecord))
	app.Post("/v1/records", h((*controllers.ApiController).AddRecord))
	app.Delete("/v1/records/:owner/:name", h((*controllers.ApiController).DeleteRecord))

	// Committing a record writes its BLOCK on the chain (object.Record.Block);
	// querying reads it back. One noun, written and read.
	app.Put("/v1/records/:owner/:name/block", h((*controllers.ApiController).CommitRecord))
	app.Get("/v1/records/:owner/:name/block", h((*controllers.ApiController).QueryRecord))

	// An asset is a resource. The collection lists and creates; the item, named
	// by its (owner, name) key, is read, replaced and removed.
	app.Get("/v1/assets", h((*controllers.ApiController).GetAssets))
	app.Post("/v1/assets", h((*controllers.ApiController).AddAsset))
	app.Get("/v1/assets/:owner/:name", h((*controllers.ApiController).GetAsset))
	app.Put("/v1/assets/:owner/:name", h((*controllers.ApiController).UpdateAsset))
	app.Delete("/v1/assets/:owner/:name", h((*controllers.ApiController).DeleteAsset))

	// A provider holds a cloud credential, so the authorization on each address
	// is the one the verb spelling had: the seam reads the (owner, name) out of
	// the path (see pathTarget) and compares it to the subject exactly as before.
	app.Get("/v1/providers", h((*controllers.ApiController).GetProviders))
	app.Post("/v1/providers", h((*controllers.ApiController).AddProvider))
	app.Get("/v1/providers/:owner/:name", h((*controllers.ApiController).GetProvider))
	app.Put("/v1/providers/:owner/:name", h((*controllers.ApiController).UpdateProvider))
	app.Delete("/v1/providers/:owner/:name", h((*controllers.ApiController).DeleteProvider))
	// A provider's rotation keys are a sub-collection: add one, and rotate or
	// remove one by name. Rotation is a PUT — setting the key's secret and/or its
	// state (active|revoked) is writing the key's own properties, not a verb of its
	// own. Verify is a POST that reads: it tests the stored credential against the
	// cloud and creates nothing. All are SuperAdmin-gated (authz.isProviderWrite).
	app.Post("/v1/providers/:owner/:name/keys", h((*controllers.ApiController).AddProviderKey))
	app.Put("/v1/providers/:owner/:name/keys/:keyName", h((*controllers.ApiController).RotateProviderKey))
	app.Delete("/v1/providers/:owner/:name/keys/:keyName", h((*controllers.ApiController).DeleteProviderKey))
	app.Post("/v1/providers/:owner/:name/verify", h((*controllers.ApiController).VerifyProvider))

	// Canonical /v1 resell compute surface — cached DigitalOcean catalog and
	// per-org machines over the configured cloud account (controllers/compute.go).
	app.Get("/v1/regions", h((*controllers.ApiController).GetComputeRegions))
	app.Get("/v1/sizes", h((*controllers.ApiController).GetComputeSizes))
	app.Get("/v1/gpus", h((*controllers.ApiController).GetComputeGPUs))
	// ONE machines collection. It answers from the organization's own providers
	// and from the house account, keyed the one way (owner, name), with Source
	// saying which. There is no second address to join against.
	app.Get("/v1/machines", h((*controllers.ApiController).ListMachines))
	app.Post("/v1/machines", h((*controllers.ApiController).LaunchComputeMachine))
	registerAgent(app)
	app.Get("/v1/machines/:owner/:name", h((*controllers.ApiController).GetMachine))
	app.Put("/v1/machines/:owner/:name", h((*controllers.ApiController).UpdateMachine))
	app.Delete("/v1/machines/:owner/:name", h((*controllers.ApiController).DeleteMachine))
	// Unified /v1/k8s noun — the ONE Kubernetes surface: DOKS cluster lifecycle
	// (list / detail+nodes / create / delete) plus the worker NODES on the fleet.
	app.Get("/v1/k8s/providers", h((*controllers.ApiController).ListComputeKubernetesProviders))
	app.Get("/v1/k8s/clusters", h((*controllers.ApiController).ListComputeKubernetesClusters))
	app.Post("/v1/k8s/clusters", h((*controllers.ApiController).CreateComputeKubernetesCluster))
	app.Get("/v1/k8s/clusters/:id", h((*controllers.ApiController).GetComputeKubernetesCluster))
	app.Delete("/v1/k8s/clusters/:id", h((*controllers.ApiController).DeleteComputeKubernetesCluster))
	// Reaching a cluster is a READ of how to reach it, minted fresh each time and
	// good for an hour. The cloud account's own key is never part of the answer.
	app.Get("/v1/k8s/clusters/:id/credentials", h((*controllers.ApiController).GetComputeKubernetesCredentials))
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

	app.Get("/v1/sessions", h((*controllers.ApiController).GetSessions))
	app.Get("/v1/sessions/:owner/:name", h((*controllers.ApiController).GetConnSession))
	app.Put("/v1/sessions/:owner/:name", h((*controllers.ApiController).UpdateSession))
	app.Post("/v1/sessions", h((*controllers.ApiController).AddSession))
	app.Delete("/v1/sessions/:owner/:name", h((*controllers.ApiController).DeleteSession))
	// Starting and stopping both write the session's STATUS — connected, or
	// disconnected. One address, because it is one property.
	app.Put("/v1/sessions/:owner/:name/status", h((*controllers.ApiController).StartSession))
	app.Delete("/v1/sessions/:owner/:name/status", h((*controllers.ApiController).StopSession))

	// Neither of these is a tunnel on an asset, which is what they were called.
	//
	// The first CREATES A SESSION for an asset and returns it, so it is a POST to
	// the asset's sessions. The second opens the live connection to a SESSION —
	// it reads ?sessionId= and upgrades to a WebSocket — so it belongs to the
	// session, not to the asset it happens to reach.
	//
	// Separating them puts each with the thing it is about: creating belongs to
	// the collection that holds the created, and the connection belongs to the
	// session it connects to.
	app.Post("/v1/assets/:owner/:name/sessions", h((*controllers.ApiController).AddAssetTunnel))
	app.Get("/v1/sessions/:owner/:name/connection", h((*controllers.ApiController).GetAssetTunnel))

	app.Get("/v1/pools", h((*controllers.ApiController).GetNodePools))
	app.Post("/v1/pools", h((*controllers.ApiController).CreateNodePool))
	app.Get("/v1/pools/:owner/:name", h((*controllers.ApiController).GetNodePool))
	app.Put("/v1/pools/:owner/:name", h((*controllers.ApiController).UpdateNodePool))
	app.Delete("/v1/pools/:owner/:name", h((*controllers.ApiController).DeleteNodePool))
	// How many nodes the pool runs is a property of the pool, so scaling is
	// writing that property — not a verb of its own.
	app.Put("/v1/pools/:owner/:name/size", h((*controllers.ApiController).ScaleNodePool))

	app.Get("/v1/plans", h((*controllers.ApiController).GetPlans))
	app.Get("/v1/plans/:owner/:name", h((*controllers.ApiController).GetPlan))
	app.Post("/v1/plans", h((*controllers.ApiController).AddPlan))
	app.Put("/v1/plans/:owner/:name", h((*controllers.ApiController).UpdatePlan))
	app.Delete("/v1/plans/:owner/:name", h((*controllers.ApiController).DeletePlan))

	app.Get("/v1/whitelabel", h((*controllers.ApiController).GetWhitelabel))

	app.Get("/v1/volumes", h((*controllers.ApiController).GetVolumes))
	app.Post("/v1/volumes", h((*controllers.ApiController).CreateVolume))
	app.Get("/v1/volumes/:owner/:name", h((*controllers.ApiController).GetVolume))
	app.Delete("/v1/volumes/:owner/:name", h((*controllers.ApiController).DeleteVolume))
	// Which machine a volume is attached to is a RELATION, and a relation the
	// volume has one of: writing it attaches, removing it detaches.
	app.Put("/v1/volumes/:owner/:name/attachment", h((*controllers.ApiController).AttachVolume))
	app.Delete("/v1/volumes/:owner/:name/attachment", h((*controllers.ApiController).DetachVolume))
	// How large the volume is, written.
	app.Put("/v1/volumes/:owner/:name/size", h((*controllers.ApiController).ResizeVolume))
}
