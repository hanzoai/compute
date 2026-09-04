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
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// declared is one op as compute DECLARES it, read off a fresh app's registry.
//
// It is a different question from registeredRoutes (router_contract_test.go):
// that one asks what compute SERVES, this asks what it DECLARES, and the gap
// between the two sets is exactly the surface no projection can see.
type declared struct {
	key     string
	in, out reflect.Type
	summary string
}

func registry(t *testing.T) []declared {
	t.Helper()
	app := zip.New(zip.Config{})
	registerHealth(app)
	registerAPI(app)

	ops := app.Registry()
	out := make([]declared, 0, len(ops))
	for _, op := range ops {
		out = append(out, declared{
			key:     op.Method + " " + op.Path,
			in:      op.InType,
			out:     op.OutType,
			summary: op.Summary,
		})
	}
	return out
}

// TestNodesIsDeclared is the whole point of registering /v1/k8s/nodes as
// zip.Get[Scope, Nodes] rather than as a handler.
//
// Being SERVED is not the property — the untyped spelling served it too. The
// property is being in the REGISTRY, because that single entry is what the
// OpenAPI document, the MCP tool list, the CLI and every generated SDK are
// derived from. A route absent from it is on the wire and nowhere else, which is
// how cloud came to hand-write a client for a shape compute never published.
func TestNodesIsDeclared(t *testing.T) {
	const want = "GET /v1/k8s/nodes"

	for _, op := range registry(t) {
		if op.key != want {
			continue
		}
		if op.in == nil || op.in.Name() != "Scope" {
			t.Errorf("%s In = %v, want controllers.Scope", want, op.in)
		}
		if op.out == nil || op.out.Name() != "Nodes" {
			t.Errorf("%s Out = %v, want controllers.Nodes", want, op.out)
		}
		if op.summary == "" {
			t.Errorf("%s has no summary — the document would publish it unnamed", want)
		}
		return
	}
	t.Fatalf("%s is not in the registry — it is served and invisible to every projection", want)
}

// TestUntypedRouteIsNotDeclared is the control, and without it the test above
// passes just as well against a registry that happened to hold every route.
//
// GET /v1/machines is the neighbouring resell list, still an untyped handler.
// That it is SERVED and NOT declared is the measurement this suite makes: the two
// sets differ, so membership in the registry means something.
func TestUntypedRouteIsNotDeclared(t *testing.T) {
	const untyped = "GET /v1/machines"

	for _, op := range registry(t) {
		if op.key == untyped {
			t.Fatalf("%s is in the registry, so this suite cannot tell a declared op from a served route", untyped)
		}
	}
	// And it really is served — otherwise "absent from the registry" would only
	// mean "absent".
	if !registeredRoutes(t)[untyped] {
		t.Fatalf("%s is not served either; this control proves nothing until it is", untyped)
	}
}

// TestDeclaredOpsAreServed closes the other direction: an op can be declared on
// an app and never reach the router. Every registry entry must be a live route.
func TestDeclaredOpsAreServed(t *testing.T) {
	served := registeredRoutes(t)
	for _, op := range registry(t) {
		if !served[op.key] {
			t.Errorf("%s is declared but not served", op.key)
		}
	}
}

// TestNodesStaysBehindTheChain is the security half, and it is not implied by any
// of the above.
//
// Health is registered AHEAD of the filter chain on purpose — a probe must reach
// its handler with no credentials — and that same spelling one line higher would
// make a tenant-scoped fleet read anonymous. Being a typed op changes nothing
// about where a route sits, so the position has to be asserted rather than
// assumed.
//
// It also drives the whole chain over a typed op for the first time: TenantContext,
// ApiFilter and the audit recorder all run, and RecordMessage's after-work reads a
// response envelope a typed handler never stashes. A 403 rather than a 500 is that
// path answering for itself.
//
// The assertion is on the BODY, not the code, and that is the point: the handler
// itself also refuses an org-less request with a 403, so a status alone cannot
// tell "the authorizer stopped it" from "it ran and found nothing to scope to".
// Only the authorizer writes "Unauthorized operation" (routers.requestDeny), so
// only that string means the request never got in.
func TestNodesStaysBehindTheChain(t *testing.T) {
	status, body := get(t, "/v1/k8s/nodes")

	if status == http.StatusOK {
		t.Fatalf("GET /v1/k8s/nodes = 200 %s — an unauthenticated fleet read was served", body)
	}
	if status != http.StatusForbidden {
		t.Fatalf("GET /v1/k8s/nodes = %d %s, want 403", status, body)
	}
	if !strings.Contains(body, "Unauthorized operation") {
		t.Fatalf("GET /v1/k8s/nodes = %s — that is the handler's own refusal, so the request reached it: the route is outside the filter chain", body)
	}
}
