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
		{"GET", "/v1/gpus"},
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
		{"POST", "/v1/regions"},           // catalog is read-only
		{"POST", "/v1/machines"},          // no bulk create on the collection
		{"DELETE", "/v1/machines/launch"}, // launch is POST-only
		{"GET", "/v1/plans"},              // not a resell-compute route
		{"GET", "/v1/get-account"},
		// Sessions are their own noun with their own predicate; the two must not
		// overlap, or one edit would widen the other.
		{"GET", "/v1/sessions"},
		{"PUT", "/v1/sessions/acme/s-1/connection"},
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

// The session noun is admitted here and org-scoped in the handler, which is a
// relocation and not a widening: the org a session belongs to is a path segment
// now, and the filter reads its target out of `?id=` or the body, so it can no
// longer see the thing it was deciding on. What these pin is the SHAPE of the
// door — which methods, and which one of them answers without a credential.
func TestSessionAdmission(t *testing.T) {
	InitAuthz()

	// The connect report is the one address that answers unauthenticated. Its
	// predecessor (POST /v1/start-session) carried the same admission, and the
	// guacamole client that sends it came through the tunnel routes, which are
	// open in the same policy, so it holds nothing to authenticate with.
	if !IsAllowed(nil, "anonymous", "anonymous", "PUT", "/v1/sessions/acme/s-1/connection", "", "") {
		t.Error("the connect report must answer without a credential")
	}
	// Everything else on the noun still needs one.
	anonymous := []struct{ method, path string }{
		{"GET", "/v1/sessions"},
		{"GET", "/v1/sessions/acme/s-1"},
		{"PUT", "/v1/sessions/acme/s-1"},
		{"DELETE", "/v1/sessions/acme/s-1"},
		{"DELETE", "/v1/sessions/acme/s-1/connection"},
	}
	for _, c := range anonymous {
		if IsAllowed(nil, "anonymous", "anonymous", c.method, c.path, "", "") {
			t.Errorf("%s %s must not answer an anonymous caller", c.method, c.path)
		}
	}

	user := &iamsdk.User{Owner: "acme", Name: "alice"}
	for _, c := range anonymous {
		if !IsAllowed(user, "acme", "alice", c.method, c.path, "", "") {
			t.Errorf("%s %s must reach an authenticated user (the handler scopes it)", c.method, c.path)
		}
	}
	// The method set is closed: nothing is POSTed to this noun, so admitting one
	// would admit a door that does not exist.
	if IsAllowed(user, "acme", "alice", "POST", "/v1/sessions", "", "") {
		t.Error("POST /v1/sessions is not a door")
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
