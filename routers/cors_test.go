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
	"github.com/zap-proto/zip/middleware"
)

// TestPreflightAllowsBearerToken drives the exact request a browser sends before
// it will call compute with a token, and asserts the answer permits it.
//
// This is a real preflight rather than an assertion over corsPolicy's fields,
// because the field being set is not the fact that matters — what matters is
// what the middleware writes into Access-Control-Allow-Headers. Tabs calls
// /v1/machines from tabs.hanzo.ai with an IAM bearer token; with Authorization
// missing from the answer, Chrome never sends the request and the whole feature
// is inert with no server-side trace of it having been tried.
func TestPreflightAllowsBearerToken(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(middleware.CORS(corsPolicy))
	registerAPI(app)

	req := httptest.NewRequest(http.MethodOptions, "/v1/machines?kind=tab", nil)
	req.Header.Set("Origin", "https://tabs.hanzo.ai")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "authorization")

	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusOK {
		t.Fatalf("preflight status = %d, want 204 or 200", res.StatusCode)
	}
	allowed := res.Header.Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(allowed), "authorization") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want it to permit Authorization", allowed)
	}
	if origin := res.Header.Get("Access-Control-Allow-Origin"); origin == "" {
		t.Fatal("Access-Control-Allow-Origin is empty, so no browser may read the response")
	}
}
