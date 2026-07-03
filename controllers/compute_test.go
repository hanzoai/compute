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
	"net/http"
	"net/http/httptest"
	"testing"

	beecontext "github.com/beego/beego/context"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
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

// botMachine fakes a launched machine as ListOrgMachines returns it: DisplayName
// is the droplet name and Tag is the comma-joined tag string carrying hanzo-kind.
func botMachine(name, kind string) *service.Machine {
	return &service.Machine{DisplayName: name, Tag: "hanzo-kind:" + kind + ",hanzo-org:acme,"}
}

// projMachine fakes a launched machine scoped to a project: Tag carries the
// hanzo-project scope tag SetScope injects at launch, alongside org and kind.
func projMachine(name, project string) *service.Machine {
	return &service.Machine{DisplayName: name, Tag: "hanzo-kind:machine,hanzo-org:acme,hanzo-project:" + project + ","}
}

// filterMachines is the ONLY grouping of a batch — no fleet entity. Launch 3
// named crawler-000..002 (kind=bot) plus one unrelated machine, then list by
// ?name= prefix, by ?kind=, and after deleting one member.
func TestFilterMachinesBatch(t *testing.T) {
	all := []*service.Machine{
		botMachine("crawler-000", service.KindBot),
		botMachine("crawler-001", service.KindBot),
		botMachine("crawler-002", service.KindBot),
		botMachine("db-1", service.KindMachine),
	}

	// ?name=crawler groups the batch (prefix match on DisplayName).
	byName := filterMachines(all, "", "crawler", "")
	if len(byName) != 3 {
		t.Fatalf("?name=crawler = %d machines, want 3", len(byName))
	}
	for i, m := range byName {
		if w := batchMemberName("crawler", i); m.DisplayName != w {
			t.Errorf("member %d = %q, want %q", i, m.DisplayName, w)
		}
	}

	// ?kind=bot selects the 3 bots and excludes the plain machine.
	if bots := filterMachines(all, service.KindBot, "", ""); len(bots) != 3 {
		t.Fatalf("?kind=bot = %d machines, want 3", len(bots))
	}
	if machines := filterMachines(all, service.KindMachine, "", ""); len(machines) != 1 || machines[0].DisplayName != "db-1" {
		t.Fatalf("?kind=machine = %v, want [db-1]", machines)
	}

	// No filter passes everything (identity).
	if len(filterMachines(all, "", "", "")) != len(all) {
		t.Fatalf("empty filter changed the list")
	}

	// Delete one member (scale down = delete): the ?name= group drops to 2.
	remaining := []*service.Machine{all[0], all[2], all[3]} // crawler-001 destroyed
	got := filterMachines(remaining, "", "crawler", "")
	if len(got) != 2 || got[0].DisplayName != "crawler-000" || got[1].DisplayName != "crawler-002" {
		t.Fatalf("after delete ?name=crawler = %v, want [crawler-000 crawler-002]", got)
	}
}

// ?project= groups a batch by the hanzo-project scope tag SetScope injected —
// the org > app > project attribution surfacing back through the list filter, and
// composing with ?kind=. A machine with no project tag never matches.
func TestFilterMachinesByProject(t *testing.T) {
	all := []*service.Machine{
		projMachine("api-1", "api"),
		projMachine("api-2", "api"),
		projMachine("web-1", "web"),
		botMachine("scopeless", service.KindMachine), // no project tag
	}

	if got := filterMachines(all, "", "", "api"); len(got) != 2 {
		t.Fatalf("?project=api = %d machines, want 2", len(got))
	}
	if got := filterMachines(all, "", "", "web"); len(got) != 1 || got[0].DisplayName != "web-1" {
		t.Fatalf("?project=web = %v, want [web-1]", got)
	}
	// project + kind compose (both must match).
	if got := filterMachines(all, service.KindMachine, "", "api"); len(got) != 2 {
		t.Fatalf("?kind=machine&project=api = %d machines, want 2", len(got))
	}
	// a machine carrying no project tag never matches a project filter.
	if got := filterMachines(all, "", "", "nope"); len(got) != 0 {
		t.Fatalf("?project=nope = %d machines, want 0 (scopeless must not match)", len(got))
	}
}

// newLaunchCtx builds a beego request context the way the router hands one to a
// handler, so resolveComputeApp/Project can read the threaded tenant scope.
func newLaunchCtx() *beecontext.Context {
	ctx := beecontext.NewContext()
	ctx.Reset(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/machines/launch", nil))
	return ctx
}

// resolveComputeApp/Project resolve the OPTIONAL scope exactly one way: the
// gateway-threaded X-App-ID / X-Project-ID tenant context wins; a body value is a
// fallback for a direct API caller; absent stays empty (a launch that omits scope
// is never broken).
func TestResolveComputeScope(t *testing.T) {
	// Threaded context is authoritative over a body fallback.
	ctx := newLaunchCtx()
	ctx.Input.SetData(object.TenantContextAppIDKey, "web")
	ctx.Input.SetData(object.TenantContextProjectIDKey, "api")
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
