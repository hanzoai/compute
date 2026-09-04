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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// get drives one real request through a fully-routed app and returns the status
// and body. Through Route rather than registerAPI, because the filter chain IS
// what these tests are about.
func get(t *testing.T, path string) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{})
	Route(app)

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return res.StatusCode, string(b)
}

// TestHealthReachesTheHandlerUnauthenticated is the point of registering health
// ahead of the chain, stated as a request.
//
// The store is not initialised in a test, so a handler that RUNS answers 503
// "store unreachable" — which is the proof. Reaching that answer with no
// credentials means the request passed the static filter, the tenant filter, the
// authorizer and the audit recorder, because none of them ran. A 403 would mean
// ApiFilter judged the probe as "anonymous", and a pod whose readiness depends
// on an authz policy is a pod that an authz mistake can take out of service.
func TestHealthReachesTheHandlerUnauthenticated(t *testing.T) {
	status, body := get(t, "/v1/health")

	if status == http.StatusForbidden {
		t.Fatalf("GET /v1/health = 403 %s — the probe was put to the policy engine", body)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("GET /v1/health = %d %s, want 503 from the handler", status, body)
	}
	if !strings.Contains(body, "store unreachable") {
		t.Fatalf("GET /v1/health body = %q, want the handler's own answer", body)
	}
}

// TestGatedRouteStillGated is the control. Without it the test above passes just
// as well when the whole filter chain is inert, which is the failure it is least
// able to notice on its own: every assertion about a bypass is only meaningful
// against a route that is NOT bypassed.
func TestGatedRouteStillGated(t *testing.T) {
	status, body := get(t, "/v1/machines")

	if status == http.StatusOK {
		t.Fatalf("GET /v1/machines = 200 %s — an unauthenticated read was served", body)
	}
	if strings.Contains(body, "store unreachable") {
		t.Fatalf("GET /v1/machines reached a handler unauthenticated: %s", body)
	}
}

// TestHealthIsDeclared proves health is a TYPED op and not merely a route. The
// registry is what every projection reads — the OpenAPI document, the MCP tool
// list, the CLI, the by-name call plane — so an op absent from it is served over
// HTTP and invisible everywhere else. That was compute's whole state before this:
// an app with no typed op projects nothing.
func TestHealthIsDeclared(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)

	for _, op := range app.Registry() {
		if op.Path == "/v1/health" {
			if op.Method != http.MethodGet {
				t.Errorf("health op method = %s, want GET", op.Method)
			}
			if op.InType == nil || op.OutType == nil {
				t.Error("health op has no In/Out type, so it projects no schema")
			}
			return
		}
	}
	t.Fatalf("no typed op declared at /v1/health; registry has %d op(s)", len(app.Registry()))
}

// TestControlPlaneNotShadowed proves static yields to the surfaces zip serves on
// compute's behalf. These are where a typed op becomes an OpenAPI document and an
// MCP tool, so a static filter that answers them first does not merely hide a
// page — it makes the whole projection unreachable while the op looks fine.
//
// Asserting "not the static filter's answer" rather than a status code, because
// the wrong answer has two shapes: 404 "not found" with no web build, and a 200
// of index.html with one. The second is the one that ships.
func TestControlPlaneNotShadowed(t *testing.T) {
	app := zip.New(zip.Config{MCP: zip.MCPConfig{Path: MCPPath}})
	Route(app)
	if _, err := zip.Serve(app, "http://127.0.0.1:0"); err != nil {
		t.Fatalf("serve: %v", err)
	}

	for _, p := range []string{zip.SpecPath, zip.DocsPath, MCPPath} {
		res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, p, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if strings.Contains(string(b), "not found") || strings.Contains(string(b), "<html") {
			t.Errorf("GET %s = %d %q — the static filter answered zip's own surface", p, res.StatusCode, b)
		}
	}
}
