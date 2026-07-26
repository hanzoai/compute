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

// tenant_context.go is the ONE source of truth for the tenant-hierarchy request
// scope (org > app > project, plus tenant/actor/env). The router filter
// (routers.TenantContextFilter) copies the gateway-injected X-*-ID headers onto
// the beego request context under these keys; every consumer (controllers) reads
// them back through the getters here. Keeping the keys and their accessors in one
// shared place — imported by both routers and controllers — means no consumer
// re-derives a key string: the scope is a value, not a place.
package object

import (
	"strings"

	"github.com/zap-proto/zip"
)

// Tenant-scope keys under which the filter stows each header on the request
// context (zip.Ctx locals). Exported so the single writer (the routers filter)
// and every reader (controllers) share the exact same key — never a duplicated
// literal.
const (
	TenantContextOrgIDKey     = "tenant.orgId"
	TenantContextAppIDKey     = "tenant.appId"
	TenantContextProjectIDKey = "tenant.projectId"
	TenantContextTenantIDKey  = "tenant.tenantId"
	TenantContextActorIDKey   = "tenant.actorId"
	TenantContextEnvKey       = "tenant.env"
)

// SetTenantContextValue stows a non-empty scope value on the request context
// locals. Empty values are skipped so an absent header leaves the key unset and
// every getter falls through to "" — the caller decides whether a missing scope
// is fatal (org) or optional (app/project).
func SetTenantContextValue(c *zip.Ctx, key, value string) {
	if c == nil || value == "" {
		return
	}
	c.Locals(key, value)
}

// GetTenantContextValue reads a scope value back, trimmed; "" when unset or when
// c is nil. This is the ONE read-back; the typed getters below are thin adapters.
func GetTenantContextValue(c *zip.Ctx, key string) string {
	if c == nil {
		return ""
	}
	text, _ := c.Locals(key).(string)
	return strings.TrimSpace(text)
}

// GetTenantOrgID returns the request's owning org scope (or "").
func GetTenantOrgID(c *zip.Ctx) string {
	return GetTenantContextValue(c, TenantContextOrgIDKey)
}

// GetTenantAppID returns the request's optional app scope beneath org (or "").
func GetTenantAppID(c *zip.Ctx) string {
	return GetTenantContextValue(c, TenantContextAppIDKey)
}

// GetTenantProjectID returns the request's optional project scope beneath app (or "").
func GetTenantProjectID(c *zip.Ctx) string {
	return GetTenantContextValue(c, TenantContextProjectIDKey)
}

// GetTenantActorID returns the request's acting principal scope (or "").
func GetTenantActorID(c *zip.Ctx) string {
	return GetTenantContextValue(c, TenantContextActorIDKey)
}

// GetTenantEnv returns the request's environment scope (or "").
func GetTenantEnv(c *zip.Ctx) string {
	return GetTenantContextValue(c, TenantContextEnvKey)
}
