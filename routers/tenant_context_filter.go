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

package routers

import (
	"strings"

	"github.com/beego/beego/context"
	"github.com/hanzoai/visor/object"
)

func normalizeTenantHeader(value string) string {
	return strings.TrimSpace(value)
}

func getTenantHeader(ctx *context.Context, name string) string {
	return normalizeTenantHeader(ctx.Input.Header(name))
}

// TenantContextFilter copies the gateway-injected tenant-scope headers
// (org > app > project, plus tenant/actor/env) onto the beego request context so
// every downstream handler resolves the caller's scope through object's getters —
// the ONE shared read-back. Org additionally falls back to the whitelabel
// hostname's org filter, and tenant defaults to org. App and project are OPTIONAL
// finer scope beneath org and are threaded verbatim (left unset when the caller
// sends no header), so a request that omits them is never altered.
func TenantContextFilter(ctx *context.Context) {
	orgID := getTenantHeader(ctx, "X-Org-ID")
	appID := getTenantHeader(ctx, "X-App-ID")
	projectID := getTenantHeader(ctx, "X-Project-ID")
	tenantID := getTenantHeader(ctx, "X-Tenant-ID")
	actorID := getTenantHeader(ctx, "X-Actor-ID")
	env := getTenantHeader(ctx, "X-Env")

	// If no explicit org header, use the whitelabel hostname's org filter.
	if orgID == "" {
		wlConfig := object.GetWhitelabelConfig(ctx.Request.Host)
		if wlConfig.OrgFilter != "" {
			orgID = wlConfig.OrgFilter
		}
	}

	if tenantID == "" {
		tenantID = orgID
	}

	object.SetTenantContextValue(ctx, object.TenantContextOrgIDKey, orgID)
	object.SetTenantContextValue(ctx, object.TenantContextAppIDKey, appID)
	object.SetTenantContextValue(ctx, object.TenantContextProjectIDKey, projectID)
	object.SetTenantContextValue(ctx, object.TenantContextTenantIDKey, tenantID)
	object.SetTenantContextValue(ctx, object.TenantContextActorIDKey, actorID)
	object.SetTenantContextValue(ctx, object.TenantContextEnvKey, env)
}
