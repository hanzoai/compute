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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beego/beego/context"
	"github.com/hanzoai/visor/object"
)

func newFilterCtx(headers map[string]string) *context.Context {
	ctx := context.NewContext()
	req := httptest.NewRequest(http.MethodPost, "/v1/machines/launch", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	ctx.Reset(httptest.NewRecorder(), req)
	return ctx
}

// TestTenantContextFilterThreadsScope proves the filter threads the full
// org > app > project scope from the gateway-injected headers onto the request
// context, and that object's getters read them back — the exact plumbing the
// compute launch handler resolves through. X-Org-ID is set so the whitelabel
// fallback branch is not exercised (that path needs config/DB).
func TestTenantContextFilterThreadsScope(t *testing.T) {
	ctx := newFilterCtx(map[string]string{
		"X-Org-ID":     "acme",
		"X-App-ID":     "web",
		"X-Project-ID": "api",
	})
	TenantContextFilter(ctx)

	if got := object.GetTenantOrgID(ctx); got != "acme" {
		t.Fatalf("org = %q, want acme", got)
	}
	if got := object.GetTenantAppID(ctx); got != "web" {
		t.Fatalf("app = %q, want web", got)
	}
	if got := object.GetTenantProjectID(ctx); got != "api" {
		t.Fatalf("project = %q, want api", got)
	}
	// tenant defaults to org when X-Tenant-ID is absent.
	if got := object.GetTenantContextValue(ctx, object.TenantContextTenantIDKey); got != "acme" {
		t.Fatalf("tenant default = %q, want acme", got)
	}
}

// TestTenantContextFilterAbsentScopeEmpty proves an omitted app/project header
// leaves the scope empty (the launch that omits it is never altered) — org still
// threads so no whitelabel lookup is triggered.
func TestTenantContextFilterAbsentScopeEmpty(t *testing.T) {
	ctx := newFilterCtx(map[string]string{"X-Org-ID": "acme"})
	TenantContextFilter(ctx)

	if got := object.GetTenantAppID(ctx); got != "" {
		t.Fatalf("absent app = %q, want empty", got)
	}
	if got := object.GetTenantProjectID(ctx); got != "" {
		t.Fatalf("absent project = %q, want empty", got)
	}
}
