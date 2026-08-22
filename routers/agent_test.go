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
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// These tests drive registerAPI alone, with no filter chain: the question is
// which route MATCHES, and the chain would answer before the router's choice
// could be observed.

// TestAgentsIsNotAMachineId pins WHICH handler answers /v1/machines/agents.
//
// The collection sits under the member's own prefix, so the literal "agents" and
// the pattern ":id" compete for it. If the pattern wins, a list of the org's
// bindings silently becomes a lookup of a machine named "agents" — and the caller
// gets a well-formed answer about the wrong thing, which is the failure worth a
// test.
//
// It does NOT test registration order, and that is the finding: fiber prefers the
// static segment however the two were registered (moving registerAgent below the
// :id routes leaves this passing), so an ordering rule here would be folklore
// dressed as a reason. What is real is the outcome, so the outcome is asserted.
//
// The proof is the SHAPE of the refusal. GetComputeMachine has no store in a test
// and answers the casibase envelope inside an HTTP 200; ListAgents is a typed op
// and refuses an anonymous caller with a real 403 and no envelope at all.
func TestAgentsIsNotAMachineId(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/machines/agents", nil))
	if err != nil {
		t.Fatalf("GET /v1/machines/agents: %v", err)
	}
	defer res.Body.Close()

	// The typed list op refuses refuseNoOrg with a real status code. The
	// untyped machine handler would answer 200 and put its failure in the body.
	if res.StatusCode == http.StatusOK {
		t.Fatal("GET /v1/machines/agents = 200 — GetComputeMachine answered, so the " +
			":id pattern captured the literal and a binding list reads as a machine lookup")
	}
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /v1/machines/agents = %d, want 403 from ListAgents", res.StatusCode)
	}
}

// TestMachineIdStillRoutes is the control. Without it the test above passes just
// as well when /v1/machines/:id has been deleted outright, which is the failure
// it is least able to notice on its own.
func TestMachineIdStillRoutes(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/machines/drop-a", nil))
	if err != nil {
		t.Fatalf("GET /v1/machines/drop-a: %v", err)
	}
	defer res.Body.Close()

	// The untyped handler ran: it answers HTTP 200 and carries its verdict in the
	// envelope. A 404 would mean the route is gone.
	if res.StatusCode == http.StatusNotFound {
		t.Fatalf("GET /v1/machines/drop-a = 404 — the machine route is not registered")
	}
}

// TestAgentBindIsPut pins the verb. The bind is idempotent, so it is a PUT on the
// machine's own agent sub-resource; the old POST .../bind-agent address is gone,
// and a caller still sending it must get a clean 404 rather than a route that
// half-works.
func TestAgentBindIsPut(t *testing.T) {
	app := zip.New(zip.Config{})
	registerAPI(app)

	gone, err := app.Fiber().Test(httptest.NewRequest(http.MethodPost, "/v1/machines/drop-a/bind-agent", nil))
	if err != nil {
		t.Fatalf("POST /v1/machines/drop-a/bind-agent: %v", err)
	}
	defer gone.Body.Close()
	if gone.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /v1/machines/drop-a/bind-agent = %d, want 404 — the old address must be gone", gone.StatusCode)
	}

	// And the new one exists: it refuses (no org context), it does not 404.
	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodPut, "/v1/machines/drop-a/agent", nil))
	if err != nil {
		t.Fatalf("PUT /v1/machines/drop-a/agent: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Fatalf("PUT /v1/machines/drop-a/agent = 404 — the bind op is not registered")
	}
}
