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

// Route mounts the whole visor HTTP surface on app: the ambient filter chain
// (recover, CORS, static, tenant, authz, record) followed by the /v1 API. The
// filter order is the Beego BeforeRouter chain preserved exactly — static short-
// circuits before the authz seam, so an asset is never gated; every /v1 route
// registered after the chain is tenant-scoped, authorized and audited.
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

func Route(app *zip.App) {
	app.Use(middleware.Recover())
	app.Use(middleware.CORS(corsPolicy))
	app.Use(zip.H(TransparentStatic))
	app.Use(zip.H(TenantContextFilter))
	app.Use(zip.H(ApiFilter))
	app.Use(zip.H(RecordMessage))

	registerAPI(app)
}

// registerAPI registers the /v1 surface — the exact route table the Beego
// namespace declared, one verb per line.
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
	// per-org machines over Hanzo's house account (controllers/compute.go).
	app.Get("/v1/regions", h((*controllers.ApiController).GetComputeRegions))
	app.Get("/v1/sizes", h((*controllers.ApiController).GetComputeSizes))
	app.Get("/v1/gpus", h((*controllers.ApiController).GetComputeGPUs))
	app.Get("/v1/machines", h((*controllers.ApiController).ListComputeMachines))
	app.Post("/v1/machines/launch", h((*controllers.ApiController).LaunchComputeMachine))
	app.Get("/v1/machines/:id", h((*controllers.ApiController).GetComputeMachine))
	app.Delete("/v1/machines/:id", h((*controllers.ApiController).DeleteComputeMachine))
	// Unified /v1/k8s noun — the ONE Kubernetes surface: DOKS cluster lifecycle
	// (list / detail+nodes / create / delete) plus the worker NODES on the fleet.
	app.Get("/v1/k8s/clusters", h((*controllers.ApiController).ListComputeKubernetesClusters))
	app.Post("/v1/k8s/clusters", h((*controllers.ApiController).CreateComputeKubernetesCluster))
	app.Get("/v1/k8s/clusters/:id", h((*controllers.ApiController).GetComputeKubernetesCluster))
	app.Delete("/v1/k8s/clusters/:id", h((*controllers.ApiController).DeleteComputeKubernetesCluster))
	app.Get("/v1/k8s/nodes", h((*controllers.ApiController).ListComputeKubernetesNodes))
	app.Get("/v1/images", h((*controllers.ApiController).ListImages))
	app.Post("/v1/images", h((*controllers.ApiController).CreateImage))

	// Agent↔machine binding — mark a machine as running the @hanzo/bot runtime for
	// a cloud Agent (controllers/agent_binding.go). Org-scoped in the controller.
	app.Post("/v1/machines/:id/bind-agent", h((*controllers.ApiController).BindAgent))
	app.Get("/v1/machines/:id/agent-binding", h((*controllers.ApiController).GetAgentBinding))
	app.Delete("/v1/machines/:id/agent-binding", h((*controllers.ApiController).UnbindAgent))
	app.Get("/v1/agent-bindings", h((*controllers.ApiController).GetAgentBindings))

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

	app.Get("/v1/get-plans", h((*controllers.ApiController).GetPlans))
	app.Get("/v1/get-plan", h((*controllers.ApiController).GetPlan))
	app.Post("/v1/add-plan", h((*controllers.ApiController).AddPlan))
	app.Post("/v1/update-plan", h((*controllers.ApiController).UpdatePlan))
	app.Post("/v1/delete-plan", h((*controllers.ApiController).DeletePlan))

	app.Get("/v1/get-whitelabel", h((*controllers.ApiController).GetWhitelabel))

	app.Get("/v1/get-volumes", h((*controllers.ApiController).GetVolumes))
	app.Get("/v1/get-volume", h((*controllers.ApiController).GetVolume))
	app.Post("/v1/create-volume", h((*controllers.ApiController).CreateVolume))
	app.Post("/v1/delete-volume", h((*controllers.ApiController).DeleteVolume))
	app.Post("/v1/attach-volume", h((*controllers.ApiController).AttachVolume))
	app.Post("/v1/detach-volume", h((*controllers.ApiController).DetachVolume))
	app.Post("/v1/resize-volume", h((*controllers.ApiController).ResizeVolume))
}
