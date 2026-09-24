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

package controllers

import (
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/compute/object"
)

// A batch launched with count=N is just N machines named "<name>-000",
// "<name>-001", … — the same "%s-%03d" scheme the deleted fleet used, now the
// ONE naming primitive for a batch.
func TestBatchMemberName(t *testing.T) {
	want := []string{"crawler-000", "crawler-001", "crawler-002"}
	for i, w := range want {
		if got := batchMemberName("crawler", i); got != w {
			t.Errorf("batchMemberName(crawler, %d) = %q, want %q", i, got, w)
		}
	}
}

// newLaunchCtx builds a ZAP request context the way the router hands one to a
// handler, so resolveComputeApp/Project can read the threaded tenant scope.
func newLaunchCtx() *zip.Ctx {
	return zip.New(zip.Config{}).TestCtx("POST", "/v1/machines")
}

// resolveComputeApp/Project resolve the OPTIONAL scope exactly one way: the
// gateway-threaded X-App-ID / X-Project-ID tenant context wins; a body value is a
// fallback for a direct API caller; absent stays empty (a launch that omits scope
// is never broken).
func TestResolveComputeScope(t *testing.T) {
	// Threaded context is authoritative over a body fallback.
	ctx := newLaunchCtx()
	ctx.Locals(object.TenantContextAppIDKey, "web")
	ctx.Locals(object.TenantContextProjectIDKey, "api")
	c := &ApiController{}
	c.Ctx = ctx
	if got := c.resolveComputeApp("bodyapp"); got != "web" {
		t.Fatalf("app: threaded X-App-ID must win, got %q want web", got)
	}
	if got := c.resolveComputeProject("bodyproj"); got != "api" {
		t.Fatalf("project: threaded X-Project-ID must win, got %q want api", got)
	}

	// No header -> the launch-body value is the fallback.
	c2 := &ApiController{}
	c2.Ctx = newLaunchCtx()
	if got := c2.resolveComputeProject("bodyproj"); got != "bodyproj" {
		t.Fatalf("project: body fallback, got %q want bodyproj", got)
	}
	if got := c2.resolveComputeApp("bodyapp"); got != "bodyapp" {
		t.Fatalf("app: body fallback, got %q want bodyapp", got)
	}

	// Neither header nor body -> empty (optional scope never gates a launch).
	c3 := &ApiController{}
	c3.Ctx = newLaunchCtx()
	if got := c3.resolveComputeProject(""); got != "" {
		t.Fatalf("project: absent must stay empty, got %q", got)
	}
	if got := c3.resolveComputeApp(""); got != "" {
		t.Fatalf("app: absent must stay empty, got %q", got)
	}
}
