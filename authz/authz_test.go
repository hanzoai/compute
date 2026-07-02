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

package authz

import "testing"

// isResellComputePath decides which routes an authenticated brand user may reach
// (org-scoping is enforced downstream in the controller). These cases pin the
// exact resell-compute surface so a future edit can't silently widen it.
func TestIsResellComputePath(t *testing.T) {
	allow := []struct{ method, path string }{
		{"GET", "/v1/regions"},
		{"GET", "/v1/sizes"},
		{"GET", "/v1/gpus"},
		{"GET", "/v1/machines"},
		{"POST", "/v1/machines/launch"},
		{"GET", "/v1/machines/abc-123"},
		{"DELETE", "/v1/machines/abc-123"},
		// Agent↔machine binding surface.
		{"GET", "/v1/agent-bindings"},
		{"GET", "/v1/machines/abc-123/agent-binding"},
		{"DELETE", "/v1/machines/abc-123/agent-binding"},
		{"POST", "/v1/machines/abc-123/bind-agent"},
	}
	for _, c := range allow {
		if !isResellComputePath(c.method, c.path) {
			t.Errorf("expected ALLOW for %s %s", c.method, c.path)
		}
	}

	deny := []struct{ method, path string }{
		{"POST", "/v1/regions"},           // catalog is read-only
		{"POST", "/v1/machines"},          // no bulk create on the collection
		{"DELETE", "/v1/machines/launch"}, // launch is POST-only
		{"GET", "/v1/plans"},              // not a resell-compute route
		{"POST", "/v1/start-session"},     // legacy surface, gated separately
		{"GET", "/v1/get-account"},
		{"PUT", "/v1/machines/abc-123"},                // only GET/DELETE by id
		{"POST", "/v1/machines/abc-123"},               // no blanket POST by id
		{"POST", "/v1/machines/abc-123/agent-binding"}, // agent-binding is GET/DELETE only
		{"PUT", "/v1/machines/abc-123/bind-agent"},     // bind-agent is POST only
		{"POST", "/v1/agent-bindings"},                 // bindings list is read-only
		{"DELETE", "/v1/agent-bindings"},               // bindings list is read-only
	}
	for _, c := range deny {
		if isResellComputePath(c.method, c.path) {
			t.Errorf("expected DENY for %s %s", c.method, c.path)
		}
	}
}
