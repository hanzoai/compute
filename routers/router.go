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
	"github.com/beego/beego"
	"github.com/hanzoai/visor/controllers"
)

func init() {
	initAPI()
}

func initAPI() {
	ns := beego.NewNamespace("/v1",
		beego.NSInclude(
			&controllers.ApiController{},
		),
	)
	beego.AddNamespace(ns)

	beego.Router("/v1/signin", &controllers.ApiController{}, "POST:Signin")
	beego.Router("/v1/signout", &controllers.ApiController{}, "POST:Signout")
	beego.Router("/v1/get-account", &controllers.ApiController{}, "GET:GetAccount")

	beego.Router("/v1/get-records", &controllers.ApiController{}, "GET:GetRecords")
	beego.Router("/v1/get-record", &controllers.ApiController{}, "GET:GetRecord")
	beego.Router("/v1/update-record", &controllers.ApiController{}, "POST:UpdateRecord")
	beego.Router("/v1/add-record", &controllers.ApiController{}, "POST:AddRecord")
	beego.Router("/v1/delete-record", &controllers.ApiController{}, "POST:DeleteRecord")

	beego.Router("/v1/commit-record", &controllers.ApiController{}, "POST:CommitRecord")
	beego.Router("/v1/query-record", &controllers.ApiController{}, "GET:QueryRecord")

	beego.Router("/v1/get-assets", &controllers.ApiController{}, "GET:GetAssets")
	beego.Router("/v1/get-asset", &controllers.ApiController{}, "GET:GetAsset")
	beego.Router("/v1/update-asset", &controllers.ApiController{}, "POST:UpdateAsset")
	beego.Router("/v1/add-asset", &controllers.ApiController{}, "POST:AddAsset")
	beego.Router("/v1/delete-asset", &controllers.ApiController{}, "POST:DeleteAsset")

	beego.Router("/v1/get-providers", &controllers.ApiController{}, "GET:GetProviders")
	beego.Router("/v1/get-provider", &controllers.ApiController{}, "GET:GetProvider")
	beego.Router("/v1/update-provider", &controllers.ApiController{}, "POST:UpdateProvider")
	beego.Router("/v1/add-provider", &controllers.ApiController{}, "POST:AddProvider")
	beego.Router("/v1/delete-provider", &controllers.ApiController{}, "POST:DeleteProvider")

	beego.Router("/v1/get-machines", &controllers.ApiController{}, "GET:GetMachines")
	beego.Router("/v1/get-machine", &controllers.ApiController{}, "GET:GetMachine")
	beego.Router("/v1/update-machine", &controllers.ApiController{}, "POST:UpdateMachine")
	beego.Router("/v1/add-machine", &controllers.ApiController{}, "POST:AddMachine")
	beego.Router("/v1/delete-machine", &controllers.ApiController{}, "POST:DeleteMachine")
	beego.Router("/v1/launch-machine", &controllers.ApiController{}, "POST:LaunchMachine")

	// Canonical /v1 resell compute surface — cached DigitalOcean catalog and
	// per-org machines over Hanzo's house account (controllers/compute.go).
	beego.Router("/v1/regions", &controllers.ApiController{}, "GET:GetComputeRegions")
	beego.Router("/v1/sizes", &controllers.ApiController{}, "GET:GetComputeSizes")
	beego.Router("/v1/gpus", &controllers.ApiController{}, "GET:GetComputeGPUs")
	beego.Router("/v1/machines", &controllers.ApiController{}, "GET:ListComputeMachines")
	beego.Router("/v1/machines/launch", &controllers.ApiController{}, "POST:LaunchComputeMachine")
	beego.Router("/v1/machines/:id", &controllers.ApiController{}, "GET:GetComputeMachine")
	beego.Router("/v1/machines/:id", &controllers.ApiController{}, "DELETE:DeleteComputeMachine")
	// Unified /v1/k8s noun — the ONE Kubernetes surface: DOKS cluster lifecycle
	// (list / detail+nodes / create / delete) plus the worker NODES on the fleet
	// (house hanzo-org cluster tag + BYOC Provider.ClusterID, deduped). All handlers
	// org-scoped in controllers/compute.go. The bare /clusters literal registers
	// before its /:id sibling so a cluster id never captures it.
	beego.Router("/v1/k8s/clusters", &controllers.ApiController{}, "get:ListComputeKubernetesClusters;post:CreateComputeKubernetesCluster")
	beego.Router("/v1/k8s/clusters/:id", &controllers.ApiController{}, "get:GetComputeKubernetesCluster;delete:DeleteComputeKubernetesCluster")
	beego.Router("/v1/k8s/nodes", &controllers.ApiController{}, "GET:ListComputeKubernetesNodes")
	beego.Router("/v1/images", &controllers.ApiController{}, "get:ListImages;post:CreateImage")

	// Agent↔machine binding — mark a machine as running the @hanzo/bot runtime for
	// a cloud Agent (controllers/agent_binding.go). Org-scoped in the controller.
	beego.Router("/v1/machines/:id/bind-agent", &controllers.ApiController{}, "POST:BindAgent")
	beego.Router("/v1/machines/:id/agent-binding", &controllers.ApiController{}, "GET:GetAgentBinding")
	beego.Router("/v1/machines/:id/agent-binding", &controllers.ApiController{}, "DELETE:UnbindAgent")
	beego.Router("/v1/agent-bindings", &controllers.ApiController{}, "GET:GetAgentBindings")

	beego.Router("/v1/get-sessions", &controllers.ApiController{}, "GET:GetSessions")
	beego.Router("/v1/get-session", &controllers.ApiController{}, "GET:GetConnSession")
	beego.Router("/v1/update-session", &controllers.ApiController{}, "POST:UpdateSession")
	beego.Router("/v1/add-session", &controllers.ApiController{}, "POST:AddSession")
	beego.Router("/v1/delete-session", &controllers.ApiController{}, "POST:DeleteSession")
	beego.Router("/v1/start-session", &controllers.ApiController{}, "POST:StartSession")
	beego.Router("/v1/stop-session", &controllers.ApiController{}, "POST:StopSession")

	beego.Router("/v1/add-asset-tunnel", &controllers.ApiController{}, "POST:AddAssetTunnel")
	beego.Router("/v1/get-asset-tunnel", &controllers.ApiController{}, "GET:GetAssetTunnel")

	beego.Router("/v1/get-node-pools", &controllers.ApiController{}, "GET:GetNodePools")
	beego.Router("/v1/get-node-pool", &controllers.ApiController{}, "GET:GetNodePool")
	beego.Router("/v1/create-node-pool", &controllers.ApiController{}, "POST:CreateNodePool")
	beego.Router("/v1/update-node-pool", &controllers.ApiController{}, "POST:UpdateNodePool")
	beego.Router("/v1/delete-node-pool", &controllers.ApiController{}, "POST:DeleteNodePool")
	beego.Router("/v1/scale-node-pool", &controllers.ApiController{}, "POST:ScaleNodePool")

	beego.Router("/v1/get-plans", &controllers.ApiController{}, "GET:GetPlans")
	beego.Router("/v1/get-plan", &controllers.ApiController{}, "GET:GetPlan")
	beego.Router("/v1/add-plan", &controllers.ApiController{}, "POST:AddPlan")
	beego.Router("/v1/update-plan", &controllers.ApiController{}, "POST:UpdatePlan")
	beego.Router("/v1/delete-plan", &controllers.ApiController{}, "POST:DeletePlan")

	beego.Router("/v1/get-whitelabel", &controllers.ApiController{}, "GET:GetWhitelabel")

	beego.Router("/v1/get-volumes", &controllers.ApiController{}, "GET:GetVolumes")
	beego.Router("/v1/get-volume", &controllers.ApiController{}, "GET:GetVolume")
	beego.Router("/v1/create-volume", &controllers.ApiController{}, "POST:CreateVolume")
	beego.Router("/v1/delete-volume", &controllers.ApiController{}, "POST:DeleteVolume")
	beego.Router("/v1/attach-volume", &controllers.ApiController{}, "POST:AttachVolume")
	beego.Router("/v1/detach-volume", &controllers.ApiController{}, "POST:DetachVolume")
	beego.Router("/v1/resize-volume", &controllers.ApiController{}, "POST:ResizeVolume")
}
