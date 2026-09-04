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

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
)

func normalizeTenantHeader(value string) string {
	return strings.TrimSpace(value)
}

func getTenantHeader(c *zip.Ctx, name string) string {
	return normalizeTenantHeader(c.Header(name))
}

// TenantContextFilter is the ZAP middleware seam: it threads the tenant scope
// onto the request, then continues the chain via c.Next(). The scope transform
// itself is applyTenantContext, kept separate from the plumbing so it is unit-
// testable without a routed context.
func TenantContextFilter(c *zip.Ctx) error {
	applyTenantContext(c)
	return c.Next()
}

// applyTenantContext copies the gateway-injected tenant-scope headers
// (org > app > project, plus tenant/actor/env) onto the request context locals
// so every downstream handler resolves the caller's scope through object's
// getters — the ONE shared read-back. Org additionally falls back to the
// whitelabel hostname's org filter, and tenant defaults to org. App and project
// are OPTIONAL finer scope beneath org and are threaded verbatim (left unset
// when the caller sends no header), so a request that omits them is never
// altered.
func applyTenantContext(c *zip.Ctx) {
	orgID := getTenantHeader(c, "X-Org-ID")
	appID := getTenantHeader(c, "X-App-ID")
	projectID := getTenantHeader(c, "X-Project-ID")
	tenantID := getTenantHeader(c, "X-Tenant-ID")
	actorID := getTenantHeader(c, "X-Actor-ID")
	env := getTenantHeader(c, "X-Env")

	// If no explicit org header, use the whitelabel hostname's org filter.
	if orgID == "" {
		wlConfig := object.GetWhitelabelConfig(c.Host())
		if wlConfig.OrgFilter != "" {
			orgID = wlConfig.OrgFilter
		}
	}

	if tenantID == "" {
		tenantID = orgID
	}

	object.SetTenantContextValue(c, object.TenantContextOrgIDKey, orgID)
	object.SetTenantContextValue(c, object.TenantContextAppIDKey, appID)
	object.SetTenantContextValue(c, object.TenantContextProjectIDKey, projectID)
	object.SetTenantContextValue(c, object.TenantContextTenantIDKey, tenantID)
	object.SetTenantContextValue(c, object.TenantContextActorIDKey, actorID)
	object.SetTenantContextValue(c, object.TenantContextEnvKey, env)
}
