// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package authz

import (
	"strings"

	authz "github.com/hanzoai/authz"
	"github.com/hanzoai/authz/model"
	stringadapter "github.com/hanzoai/authz/persist/string-adapter"
	"github.com/hanzoai/iamsdk/v2/iamsdk"
	"github.com/hanzoai/visor/conf"
)

var Enforcer *authz.Enforcer

func InitAuthz() {
	var err error

	modelText := `
[request_definition]
r = subOwner, subName, method, urlPath, objOwner, objName

[policy_definition]
p = subOwner, subName, method, urlPath, objOwner, objName

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (r.subOwner == p.subOwner || p.subOwner == "*") && (r.subName == p.subName || p.subName == "*") && \
  (r.method == p.method || p.method == "*") && (r.urlPath == p.urlPath || p.urlPath == "*") && \
  (r.objOwner == p.objOwner || p.objOwner == "*") && (r.objName == p.objName || p.objName == "*")
`

	m, err := model.NewModelFromString(modelText)
	if err != nil {
		panic(err)
	}

	// The policy is STATIC (code-defined below) — it lives entirely in memory. No
	// database (was a pointless Postgres api_rule persistence sink for hardcoded
	// rules; platform rule: no SQL). One source of truth: the ruleText string adapter.
	Enforcer, err = authz.NewEnforcer(m)
	if err != nil {
		panic(err)
	}

	if true {
		ruleText := `
p, built-in, *, *, *, *, *
p, app, *, *, *, *, *
p, *, *, POST, /v1/signin, *, *
p, *, *, POST, /v1/signout, *, *
p, *, *, GET, /v1/get-account, *, *
p, *, *, GET, /v1/get-asset-tunnel, *, *
p, *, *, POST, /v1/add-asset-tunnel, *, *
p, *, *, POST, /v1/start-session, *, *
p, *, *, GET, /v1/get-whitelabel, *, *
p, *, *, GET, /v1/regions, *, *
p, *, *, GET, /v1/sizes, *, *
p, *, *, GET, /v1/gpus, *, *
`

		sa := stringadapter.NewAdapter(ruleText)
		// Load the static rules into the enforcer's memory. There is no DB to save
		// them back to — the rules are the source of truth in code.
		if err := sa.LoadPolicy(Enforcer.GetModel()); err != nil {
			panic(err)
		}
	}
}

func IsAllowed(user *iamsdk.User, subOwner string, subName string, method string, urlPath string, objOwner string, objName string) bool {
	if conf.GetConfigBool("IsDemoMode") {
		if !isAllowedInDemoMode(method, urlPath) {
			return false
		}
	}

	if subOwner == "app" {
		return true
	}

	if user != nil {
		if user.IsDeleted {
			return false
		}

		// Resell compute surface (catalog + platform-account machines) is org-scoped
		// IN THE CONTROLLER: resolveComputeOrg pins org = user.Owner for real
		// users (a client-supplied ?owner is ignored), so a maxpower user can only
		// ever list/launch/destroy maxpower's machines. Any authenticated user of
		// this brand may therefore reach these routes; cross-org access is
		// impossible regardless of objOwner. This is what lets a customer see and
		// manage their OWN machines (not just browse the catalog) without the
		// request having to carry a matching ?owner for the subOwner==objOwner rule.
		if isResellComputePath(method, urlPath) {
			return true
		}

		// A subject may act on its OWN org's objects. There is no second clause:
		// this used to also return true whenever the OBJECT's owner was "admin",
		// which admitted every authenticated customer to every object of the
		// reserved SuperAdmin org — the object's owner is not a statement about the
		// subject. A member of the admin org still passes, through this same
		// subOwner == objOwner comparison, because that is what membership means.
		if subOwner == objOwner {
			return true
		}
	}

	res, err := Enforcer.Enforce(subOwner, subName, method, urlPath, objOwner, objName)
	if err != nil {
		panic(err)
	}

	return res
}

// isResellComputePath reports whether (method, urlPath) is a resell-compute
// route whose org-scoping is enforced downstream in the controller
// (resolveComputeOrg). These are safe to admit for any authenticated user of the
// brand:
//
//	GET    /v1/regions|/v1/sizes|/v1/gpus     catalog (also public-read)
//	GET    /v1/machines                       list the caller org's machines
//	POST   /v1/machines/launch                quote (dryRun) or metered launch
//	GET    /v1/machines/<id>                  get one of the caller org's machines
//	DELETE /v1/machines/<id>                  destroy one of the caller org's machines
//	GET    /v1/machines/agents                list the caller org's agent bindings
//	GET    /v1/machines/<id>/agent            read a machine's agent binding
//	DELETE /v1/machines/<id>/agent            unbind a machine's agent
//	PUT    /v1/machines/<id>/agent            bind a cloud Agent to a machine
func isResellComputePath(method string, urlPath string) bool {
	switch urlPath {
	case "/v1/regions", "/v1/sizes", "/v1/gpus", "/v1/machines":
		return method == "GET"
	case "/v1/machines/launch":
		return method == "POST"
	}
	// Agent↔machine binding: the bind is the ONE write admitted here, and only as
	// PUT on the machine's own agent sub-resource. Every other write under
	// /v1/machines/ stays denied — no blanket POST or PUT on the prefix.
	if method == "PUT" && strings.HasPrefix(urlPath, "/v1/machines/") && strings.HasSuffix(urlPath, "/agent") {
		return true
	}
	// /v1/machines/<id>, /v1/machines/<id>/agent and /v1/machines/agents — read
	// or delete a specific machine, its binding, or the org's binding list.
	if strings.HasPrefix(urlPath, "/v1/machines/") {
		return method == "GET" || method == "DELETE"
	}
	return false
}

func isAllowedInDemoMode(method string, urlPath string) bool {
	if method == "POST" {
		if strings.HasPrefix(urlPath, "/v1/signin") || urlPath == "/v1/signout" || urlPath == "/v1/add-asset-tunnel" || urlPath == "/v1/start-session" || urlPath == "/v1/stop-session" {
			return true
		} else {
			return false
		}
	}

	// If method equals GET
	return true
}
