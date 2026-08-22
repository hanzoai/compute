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

package routers

import (
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/visor/object"
)

// newFilterCtx builds a ZAP request context the way the router hands one to the
// filter chain, with the gateway-injected tenant headers set.
func newFilterCtx(app *zip.App, headers map[string]string) *zip.Ctx {
	c := app.TestCtx("POST", "/v1/machines")
	for k, v := range headers {
		c.Fiber().Request().Header.Set(k, v)
	}
	return c
}

// TestTenantContextFilterThreadsScope proves the filter threads the full
// org > app > project scope from the gateway-injected headers onto the request
// context, and that object's getters read them back — the exact plumbing the
// compute launch handler resolves through. X-Org-ID is set so the whitelabel
// fallback branch is not exercised (that path needs config/DB).
func TestTenantContextFilterThreadsScope(t *testing.T) {
	app := zip.New(zip.Config{})
	c := newFilterCtx(app, map[string]string{
		"X-Org-ID":     "acme",
		"X-App-ID":     "web",
		"X-Project-ID": "api",
	})
	applyTenantContext(c)

	if got := object.GetTenantOrgID(c); got != "acme" {
		t.Fatalf("org = %q, want acme", got)
	}
	if got := object.GetTenantAppID(c); got != "web" {
		t.Fatalf("app = %q, want web", got)
	}
	if got := object.GetTenantProjectID(c); got != "api" {
		t.Fatalf("project = %q, want api", got)
	}
	// tenant defaults to org when X-Tenant-ID is absent.
	if got := object.GetTenantContextValue(c, object.TenantContextTenantIDKey); got != "acme" {
		t.Fatalf("tenant default = %q, want acme", got)
	}
}

// TestTenantContextFilterAbsentScopeEmpty proves an omitted app/project header
// leaves the scope empty (the launch that omits it is never altered) — org still
// threads so no whitelabel lookup is triggered.
func TestTenantContextFilterAbsentScopeEmpty(t *testing.T) {
	app := zip.New(zip.Config{})
	c := newFilterCtx(app, map[string]string{"X-Org-ID": "acme"})
	applyTenantContext(c)

	if got := object.GetTenantAppID(c); got != "" {
		t.Fatalf("absent app = %q, want empty", got)
	}
	if got := object.GetTenantProjectID(c); got != "" {
		t.Fatalf("absent project = %q, want empty", got)
	}
}
