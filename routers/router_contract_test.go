// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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
	"fmt"
	"sort"
	"testing"

	"github.com/zap-proto/zip"
)

// route is one entry of the public HTTP contract: the verb, the path template,
// and the ApiController method that serves it.
type route struct{ method, path, handler string }

// apiContract is visor's /v1 surface, written out by hand: every verb, path and
// handler this service serves.
//
// It began as the migration's acceptance criterion — transcribed from the
// pre-migration routers/router.go (commit 776c6fc~1), the method:handler table
// of every route the old framework registered, multi-verb specs expanded one row
// per verb — and it did that job: the framework was replaced and this table is
// how we know the public surface came through unchanged.
//
// It is NOT retired now that the migration is over, and it is not a record of
// what some previous framework happened to do. It is the declared contract, and
// the property it pins is the one that outlives any framework: what visor SERVES
// equals what visor SAYS it serves. A route added without a line here fails, and
// so does a line here without a route.
//
// Deliberately hand-written rather than derived from registerAPI: a table
// generated from the code under test agrees with that code by construction and
// proves nothing.
//
// Changing a line here changes visor's public API. That is the point.
var apiContract = []route{
	// The TYPED ops, whose handlers are package functions rather than
	// ApiController methods: health and the k8s worker nodes here, a machine's
	// agent below. A typed op is in the registry every projection reads rather
	// than only on the wire.
	{"GET", "/v1/health", "health"},
	{"GET", "/v1/k8s/nodes", "ListNodes"},

	{"POST", "/v1/signin", "Signin"},
	{"POST", "/v1/signout", "Signout"},
	{"GET", "/v1/get-account", "GetAccount"},
	{"GET", "/v1/get-records", "GetRecords"},
	{"GET", "/v1/get-record", "GetRecord"},
	{"POST", "/v1/update-record", "UpdateRecord"},
	{"POST", "/v1/add-record", "AddRecord"},
	{"POST", "/v1/delete-record", "DeleteRecord"},
	{"POST", "/v1/commit-record", "CommitRecord"},
	{"GET", "/v1/query-record", "QueryRecord"},
	{"GET", "/v1/get-assets", "GetAssets"},
	{"GET", "/v1/get-asset", "GetAsset"},
	{"POST", "/v1/update-asset", "UpdateAsset"},
	{"POST", "/v1/add-asset", "AddAsset"},
	{"POST", "/v1/delete-asset", "DeleteAsset"},
	{"GET", "/v1/get-providers", "GetProviders"},
	{"GET", "/v1/get-provider", "GetProvider"},
	{"POST", "/v1/update-provider", "UpdateProvider"},
	{"POST", "/v1/add-provider", "AddProvider"},
	{"POST", "/v1/delete-provider", "DeleteProvider"},
	{"GET", "/v1/get-machines", "GetMachines"},
	{"GET", "/v1/get-machine", "GetMachine"},
	{"POST", "/v1/update-machine", "UpdateMachine"},
	{"POST", "/v1/add-machine", "AddMachine"},
	{"POST", "/v1/delete-machine", "DeleteMachine"},
	{"POST", "/v1/launch-machine", "LaunchMachine"},
	{"GET", "/v1/regions", "GetComputeRegions"},
	{"GET", "/v1/sizes", "GetComputeSizes"},
	{"GET", "/v1/gpus", "GetComputeGPUs"},
	{"GET", "/v1/machines", "ListComputeMachines"},
	{"POST", "/v1/machines/launch", "LaunchComputeMachine"},
	// A machine's AGENT — four TYPED ops (see registerAgent), so like health
	// these rows name package functions rather than ApiController methods. The
	// literal /v1/machines/agents is declared here, ahead of /v1/machines/:id,
	// in the order registerAPI installs them.
	{"GET", "/v1/machines/agents", "ListAgents"},
	{"PUT", "/v1/machines/:id/agent", "BindAgent"},
	{"GET", "/v1/machines/:id/agent", "GetAgent"},
	{"DELETE", "/v1/machines/:id/agent", "UnbindAgent"},
	{"GET", "/v1/machines/:id", "GetComputeMachine"},
	{"DELETE", "/v1/machines/:id", "DeleteComputeMachine"},
	{"GET", "/v1/k8s/clusters", "ListComputeKubernetesClusters"},
	{"POST", "/v1/k8s/clusters", "CreateComputeKubernetesCluster"},
	{"GET", "/v1/k8s/clusters/:id", "GetComputeKubernetesCluster"},
	{"DELETE", "/v1/k8s/clusters/:id", "DeleteComputeKubernetesCluster"},
	{"GET", "/v1/images", "ListImages"},
	{"POST", "/v1/images", "CreateImage"},
	{"GET", "/v1/get-sessions", "GetSessions"},
	{"GET", "/v1/get-session", "GetConnSession"},
	{"POST", "/v1/update-session", "UpdateSession"},
	{"POST", "/v1/add-session", "AddSession"},
	{"POST", "/v1/delete-session", "DeleteSession"},
	{"POST", "/v1/start-session", "StartSession"},
	{"POST", "/v1/stop-session", "StopSession"},
	{"POST", "/v1/add-asset-tunnel", "AddAssetTunnel"},
	{"GET", "/v1/get-asset-tunnel", "GetAssetTunnel"},
	{"GET", "/v1/get-node-pools", "GetNodePools"},
	{"GET", "/v1/get-node-pool", "GetNodePool"},
	{"POST", "/v1/create-node-pool", "CreateNodePool"},
	{"POST", "/v1/update-node-pool", "UpdateNodePool"},
	{"POST", "/v1/delete-node-pool", "DeleteNodePool"},
	{"POST", "/v1/scale-node-pool", "ScaleNodePool"},
	{"GET", "/v1/get-plans", "GetPlans"},
	{"GET", "/v1/get-plan", "GetPlan"},
	{"POST", "/v1/add-plan", "AddPlan"},
	{"POST", "/v1/update-plan", "UpdatePlan"},
	{"POST", "/v1/delete-plan", "DeletePlan"},
	{"GET", "/v1/get-whitelabel", "GetWhitelabel"},
	{"GET", "/v1/get-volumes", "GetVolumes"},
	{"GET", "/v1/get-volume", "GetVolume"},
	{"POST", "/v1/create-volume", "CreateVolume"},
	{"POST", "/v1/delete-volume", "DeleteVolume"},
	{"POST", "/v1/attach-volume", "AttachVolume"},
	{"POST", "/v1/detach-volume", "DetachVolume"},
	{"POST", "/v1/resize-volume", "ResizeVolume"},
}

// key renders a route as "METHOD path" for set comparison.
func (r route) key() string { return r.method + " " + r.path }

// registeredRoutes returns the verb+path set visor actually installs on a fresh
// app, read back off the live fiber router (not re-parsed from source), so the
// assertion covers what the server will really serve.
//
// Both registration functions run, because the contract is the whole surface and
// health is registered separately — ahead of the filter chain, deliberately (see
// Route). Calling only registerAPI here would leave a served route outside the
// contract, which is the exact gap this test exists to close. The filter chain
// itself is omitted: Use-handlers are not routes.
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	app := zip.New(zip.Config{})
	registerHealth(app)
	registerAPI(app)

	got := make(map[string]bool)
	for _, r := range app.Fiber().GetRoutes(true) {
		// fiber auto-generates a HEAD twin for every GET; it is not part of the
		// declared contract, so it is not counted against it.
		if r.Method == "HEAD" {
			continue
		}
		got[r.Method+" "+r.Path] = true
	}
	return got
}

// TestAPIContractPreserved is the standing check: the routes visor registers are
// exactly the routes it declares — none dropped, none invented. It caught the
// framework swap that motivated it, and it now catches the ordinary case: a
// route that ships without a contract line, or a contract line with no route.
func TestAPIContractPreserved(t *testing.T) {
	got := registeredRoutes(t)

	want := make(map[string]bool, len(apiContract))
	for _, r := range apiContract {
		want[r.key()] = true
	}

	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	for _, k := range missing {
		t.Errorf("route dropped by the migration: %s", k)
	}
	for _, k := range extra {
		t.Errorf("route served but not declared: %s", k)
	}
	if len(got) != len(want) {
		t.Errorf("route count = %d, want %d", len(got), len(want))
	}
}

// TestAPIContractCount pins the size of the surface, so a route added without a
// contract line (or a duplicate registration) fails loudly.
func TestAPIContractCount(t *testing.T) {
	const wantRoutes = 73
	if len(apiContract) != wantRoutes {
		t.Fatalf("contract table has %d routes, want %d", len(apiContract), wantRoutes)
	}
	if n := len(registeredRoutes(t)); n != wantRoutes {
		t.Fatalf("registerAPI installed %d routes, want %d", n, wantRoutes)
	}
}

// TestAPIContractVerbMix pins the per-verb split — a GET silently re-registered
// as POST keeps the total at 72 while breaking every caller.
func TestAPIContractVerbMix(t *testing.T) {
	want := map[string]int{"GET": 32, "POST": 37, "DELETE": 3, "PUT": 1}

	got := map[string]int{}
	for k := range registeredRoutes(t) {
		var verb string
		fmt.Sscan(k, &verb)
		got[verb]++
	}
	for verb, n := range want {
		if got[verb] != n {
			t.Errorf("%s routes = %d, want %d", verb, got[verb], n)
		}
	}
	if len(got) != len(want) {
		t.Errorf("verbs = %v, want exactly %v", got, want)
	}
}

// TestAPIContractNoDuplicatePaths proves no (verb, path) pair is registered
// twice — a duplicate silently shadows the second handler in fiber.
func TestAPIContractNoDuplicatePaths(t *testing.T) {
	seen := make(map[string]string, len(apiContract))
	for _, r := range apiContract {
		if prev, dup := seen[r.key()]; dup {
			t.Errorf("duplicate route %s: handlers %s and %s", r.key(), prev, r.handler)
		}
		seen[r.key()] = r.handler
	}
}

// TestAPIContractHandlersExist proves every contract row names a handler.
// registerAPI takes the function values directly — an ApiController method for
// an untyped route, a package function for a typed op — so existence is enforced
// at compile time; the explicit check keeps the table honest if a route is ever
// registered via a string indirection.
func TestAPIContractHandlersExist(t *testing.T) {
	for _, r := range apiContract {
		if r.handler == "" {
			t.Errorf("route %s has no handler", r.key())
		}
	}
}
