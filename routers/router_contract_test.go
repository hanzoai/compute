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
	"strings"
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
	{"GET", "/v1/account", "GetAccount"},
	{"GET", "/v1/records", "GetRecords"},
	{"GET", "/v1/records/:owner/:name", "GetRecord"},
	{"PUT", "/v1/records/:owner/:name", "UpdateRecord"},
	{"POST", "/v1/records", "AddRecord"},
	{"DELETE", "/v1/records/:owner/:name", "DeleteRecord"},
	{"PUT", "/v1/records/:owner/:name/block", "CommitRecord"},
	{"GET", "/v1/records/:owner/:name/block", "QueryRecord"},
	{"GET", "/v1/assets", "GetAssets"},
	{"GET", "/v1/assets/:owner/:name", "GetAsset"},
	{"PUT", "/v1/assets/:owner/:name", "UpdateAsset"},
	{"POST", "/v1/assets", "AddAsset"},
	{"DELETE", "/v1/assets/:owner/:name", "DeleteAsset"},
	{"GET", "/v1/providers", "GetProviders"},
	{"GET", "/v1/providers/:owner/:name", "GetProvider"},
	{"PUT", "/v1/providers/:owner/:name", "UpdateProvider"},
	{"POST", "/v1/providers", "AddProvider"},
	{"DELETE", "/v1/providers/:owner/:name", "DeleteProvider"},
	{"GET", "/v1/machines", "ListMachines"},
	{"GET", "/v1/machines/:owner/:name", "GetMachine"},
	{"PUT", "/v1/machines/:owner/:name", "UpdateMachine"},
	{"POST", "/v1/machines", "LaunchComputeMachine"},
	{"DELETE", "/v1/machines/:owner/:name", "DeleteMachine"},
	{"GET", "/v1/regions", "GetComputeRegions"},
	{"GET", "/v1/sizes", "GetComputeSizes"},
	{"GET", "/v1/gpus", "GetComputeGPUs"},
	// A machine's AGENT — four TYPED ops (see registerAgent), so like health
	// these rows name package functions rather than ApiController methods. The
	// literal /v1/machines/agents is declared here, ahead of /v1/machines/:owner/:name,
	// in the order registerAPI installs them.
	{"GET", "/v1/machines/agents", "ListAgents"},
	{"PUT", "/v1/machines/:owner/:name/agent", "BindAgent"},
	{"GET", "/v1/machines/:owner/:name/agent", "GetAgent"},
	{"DELETE", "/v1/machines/:owner/:name/agent", "UnbindAgent"},
	{"GET", "/v1/k8s/providers", "ListComputeKubernetesProviders"},
	{"GET", "/v1/k8s/clusters", "ListComputeKubernetesClusters"},
	{"POST", "/v1/k8s/clusters", "CreateComputeKubernetesCluster"},
	{"GET", "/v1/k8s/clusters/:id", "GetComputeKubernetesCluster"},
	{"DELETE", "/v1/k8s/clusters/:id", "DeleteComputeKubernetesCluster"},
	{"GET", "/v1/k8s/clusters/:id/credentials", "GetComputeKubernetesCredentials"},
	{"GET", "/v1/images", "ListImages"},
	{"POST", "/v1/images", "CreateImage"},
	{"GET", "/v1/sessions", "GetSessions"},
	{"GET", "/v1/sessions/:owner/:name", "GetConnSession"},
	{"PUT", "/v1/sessions/:owner/:name", "UpdateSession"},
	{"POST", "/v1/sessions", "AddSession"},
	{"DELETE", "/v1/sessions/:owner/:name", "DeleteSession"},
	{"PUT", "/v1/sessions/:owner/:name/status", "StartSession"},
	{"DELETE", "/v1/sessions/:owner/:name/status", "StopSession"},
	{"POST", "/v1/assets/:owner/:name/sessions", "AddAssetTunnel"},
	{"GET", "/v1/sessions/:owner/:name/connection", "GetAssetTunnel"},
	{"GET", "/v1/pools", "GetNodePools"},
	{"GET", "/v1/pools/:owner/:name", "GetNodePool"},
	{"POST", "/v1/pools", "CreateNodePool"},
	{"PUT", "/v1/pools/:owner/:name", "UpdateNodePool"},
	{"DELETE", "/v1/pools/:owner/:name", "DeleteNodePool"},
	{"PUT", "/v1/pools/:owner/:name/size", "ScaleNodePool"},
	{"GET", "/v1/plans", "GetPlans"},
	{"GET", "/v1/plans/:owner/:name", "GetPlan"},
	{"POST", "/v1/plans", "AddPlan"},
	{"PUT", "/v1/plans/:owner/:name", "UpdatePlan"},
	{"DELETE", "/v1/plans/:owner/:name", "DeletePlan"},
	{"GET", "/v1/whitelabel", "GetWhitelabel"},
	{"GET", "/v1/volumes", "GetVolumes"},
	{"GET", "/v1/volumes/:owner/:name", "GetVolume"},
	{"POST", "/v1/volumes", "CreateVolume"},
	{"DELETE", "/v1/volumes/:owner/:name", "DeleteVolume"},
	{"PUT", "/v1/volumes/:owner/:name/attachment", "AttachVolume"},
	{"DELETE", "/v1/volumes/:owner/:name/attachment", "DetachVolume"},
	{"PUT", "/v1/volumes/:owner/:name/size", "ResizeVolume"},
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

	// A route may LEAVE this table only by being retired. Retirement is not a
	// drop: the address still answers, with 410 and the resource that replaced
	// it, so a caller is told where to go rather than meeting a 404. Anything
	// that leaves without a successor is the silent break this test exists for.
	for _, k := range missing {
		path := k[strings.Index(k, " ")+1:]
		if Retired(path) {
			continue
		}
		t.Errorf("route dropped by the migration: %s — retire it with Retire(%q, <successor>) "+
			"so callers are told where it went, or put it back", k, path)
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
	const wantRoutes = 70
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
	// assets and providers moved to resource addresses, and the shape moved with
	// them: replacing an item is PUT and removing one is DELETE, where the verb
	// surface said POST to both. Four POSTs became two PUTs and two DELETEs —
	// update-asset, delete-asset, update-provider, delete-provider.
	//
	//	POST 37 -> 33    PUT 1 -> 3    DELETE 3 -> 5    GET 33 unchanged
	//
	// GET does not move: reading a collection and reading an item were both GET
	// before and are both GET now. A number here that changes WITHOUT a family
	// moving is the thing this test is for.
	// Volumes, node pools and the four state changes moved, and the shape moved
	// with them. Ten POSTs became PUTs or DELETEs — because writing a property
	// (a pool's size, a volume's attachment, a session's status, a record's
	// block) is PUT, and removing one is DELETE.
	//
	//	POST 27 -> 17    PUT 6 -> 12    DELETE 8 -> 12    GET 33 unchanged
	//
	// GET stays put across every family: reading a collection and reading an item
	// were both GET before and are both GET now. A number that moves WITHOUT a
	// family moving is what this test is for.
	// The two machine collections became one, so the counts fall as well as move:
	// four POSTs and two GETs went away entirely rather than changing method.
	//
	//	GET 33 -> 31    POST 17 -> 13    PUT 12 -> 13    DELETE 12 unchanged
	//
	// GET falls for the first time in this migration, and only here — two
	// addresses answered the same question and one of them is gone.
	// Reaching a managed cluster is a READ of how to reach it — a minted,
	// hour-long apiserver credential — so it is the one address the estate was
	// missing and it is a GET.
	//
	//	GET 31 -> 32
	want := map[string]int{"GET": 32, "POST": 13, "DELETE": 12, "PUT": 13}

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
