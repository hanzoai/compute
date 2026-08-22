// Copyright 2025 Hanzo Industries Inc. All Rights Reserved.
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
	"testing"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// isResellComputePath decides which routes an authenticated brand user may reach
// (org-scoping is enforced downstream in the controller). These cases pin the
// exact resell-compute surface so a future edit can't silently widen it.
func TestIsResellComputePath(t *testing.T) {
	allow := []struct{ method, path string }{
		{"GET", "/v1/regions"},
		{"GET", "/v1/sizes"},
		// The image catalog: read what this org may launch, register a custom
		// image into it. Org-scoped in the controller, like the rest.
		{"GET", "/v1/images"},
		{"POST", "/v1/images"},
		{"GET", "/v1/machines"},
		{"POST", "/v1/machines/launch"},
		{"GET", "/v1/machines/abc-123"},
		{"DELETE", "/v1/machines/abc-123"},
		// A machine's agent — one address, the method carrying the verb.
		{"GET", "/v1/machines/agents"},
		{"GET", "/v1/machines/abc-123/agent"},
		{"DELETE", "/v1/machines/abc-123/agent"},
		{"PUT", "/v1/machines/abc-123/agent"},
	}
	for _, c := range allow {
		if !isResellComputePath(c.method, c.path) {
			t.Errorf("expected ALLOW for %s %s", c.method, c.path)
		}
	}

	deny := []struct{ method, path string }{
		{"POST", "/v1/regions"},                     // catalog is read-only
		{"GET", "/v1/gpus"},                         // retired: the GPU sizes are /v1/sizes?gpu=true
		{"DELETE", "/v1/images"},                    // the catalog is not deleted wholesale
		{"POST", "/v1/machines"},                    // no bulk create on the collection
		{"DELETE", "/v1/machines/launch"},           // launch is POST-only
		{"GET", "/v1/plans"},                        // not a resell-compute route
		{"POST", "/v1/start-session"},               // legacy surface, gated separately
		{"GET", "/v1/account"},                      // admitted by the static policy, not by this rule
		{"PUT", "/v1/machines/abc-123"},             // only GET/DELETE by id
		{"POST", "/v1/machines/abc-123"},            // no blanket POST by id
		{"POST", "/v1/machines/abc-123/agent"},      // the bind is PUT, and only PUT
		{"PUT", "/v1/machines/abc-123/tag"},         // PUT is admitted for /agent alone
		{"POST", "/v1/machines/agents"},             // the binding list is read-only
		{"PUT", "/v1/machines/agents"},              // …and is not a bind target
		{"GET", "/v1/agent-bindings"},               // the old list address is gone
		{"POST", "/v1/machines/abc-123/bind-agent"}, // the old bind address is gone
	}
	for _, c := range deny {
		if isResellComputePath(c.method, c.path) {
			t.Errorf("expected DENY for %s %s", c.method, c.path)
		}
	}
}

// The object's owner is not a statement about the subject. IsAllowed used to
// return true for ANY authenticated user whenever the OBJECT's owner was "admin"
// — the reserved SuperAdmin org — so every customer of the brand was admitted to
// every SuperAdmin-owned object on every route the static policy did not cover.
//
// A member of the admin org still passes, because subOwner == objOwner is what
// membership means. That is the whole rule; there is no second clause.
func TestAdminOwnedObjectIsNotEveryonesObject(t *testing.T) {
	InitAuthz()

	customer := &iamsdk.User{Owner: "acme", Name: "alice"}
	superadmin := &iamsdk.User{Owner: "admin", Name: "z"}

	// A route the static policy does not cover, so the decision is the subject
	// rule alone.
	const method, path = "POST", "/v1/create-node-pool"

	if IsAllowed(customer, "acme", "alice", method, path, "admin", "thing") {
		t.Fatal("a customer must not reach a SuperAdmin-owned object")
	}
	if !IsAllowed(superadmin, "admin", "z", method, path, "admin", "thing") {
		t.Fatal("a member of the admin org must still reach its own org's objects")
	}
	if !IsAllowed(customer, "acme", "alice", method, path, "acme", "thing") {
		t.Fatal("a customer must still reach its OWN org's objects")
	}
	if IsAllowed(customer, "acme", "alice", method, path, "victim", "thing") {
		t.Fatal("a customer must not reach another customer's objects")
	}
}
