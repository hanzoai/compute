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
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// TestComputerIsDeclared proves the address is a TYPED op, not merely a route:
// the registry is what OpenAPI, MCP, the CLI and zip.Here all read. The wire is
// still verbatim over REST — that half is pinned in controllers/computer_test.go.
func TestComputerIsDeclared(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)

	const want = "POST /v1/machines/:owner/:name/computer"
	for _, op := range app.Registry() {
		if op.Method+" "+op.Path != want {
			continue
		}
		if op.InType == nil || op.OutType == nil {
			t.Error("computer op has no In/Out type, so it projects no schema")
		}
		if op.OperationID != "machineComputer" {
			t.Errorf("operation id = %q, want machineComputer", op.OperationID)
		}
		summary, desc, ok := zip.Prose(op.Method, op.Path)
		if !ok || summary == "" || desc == "" {
			t.Errorf("no generated prose for %s — zipdoc_gen.go is stale or missing", want)
		}
		return
	}
	t.Fatalf("no typed op at %s; registry has %d op(s)", want, len(app.Registry()))
}

// TestComputerIsGated proves the op sits INSIDE the authz chain. Health is
// deliberately ahead of it; a computer op must not be, since its act half is
// approved only on evidence Cloud verified at its own boundary.
func TestComputerIsGated(t *testing.T) {
	app := zip.New(zip.Config{})
	Route(app)
	req := httptest.NewRequest(http.MethodPost, "/v1/machines/acme/mac-1/computer", strings.NewReader(`{"approval":true}`))
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("POST computer: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated computer op = %d, want 403", res.StatusCode)
	}
}
