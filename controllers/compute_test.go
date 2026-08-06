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
	"encoding/json"
	"testing"

	"github.com/zap-proto/zip"

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

// newLaunchCtx builds a ZAP request context the way the router hands one to a
// handler, so resolveComputeApp/Project can read the threaded tenant scope.
func newLaunchCtx() *zip.Ctx {
	return zip.New(zip.Config{}).TestCtx("POST", "/v1/machines/launch")
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

// unionMachines merges the two DOKS node sources (house hanzo-org tag + BYOC
// Provider.ClusterID) into ONE deduped fleet list. A DOKS-only node surfaces; a
// node whose droplet is ALSO in the droplet list dedups by droplet id; a
// same-name collision dedups by name; the FIRST source wins so its row is kept.
func TestUnionMachinesDedup(t *testing.T) {
	// Source 1 (house): the authoritative rows — a DOKS node and a droplet already
	// on the fleet.
	house := []*service.Machine{
		{Id: "111", Name: "prod-default-aaa", Provider: "DigitalOcean", Tag: "doks-cluster:prod"},
		{Id: "999", Name: "web-1", Provider: "DigitalOcean"},
	}
	// Source 2 (BYOC): the SAME droplet 111 again (must dedup by id, house wins),
	// a row that collides by NAME only (web-1, no id overlap → dedup by name), and a
	// genuinely new BYOC-only node 222.
	byoc := []*service.Machine{
		{Id: "111", Name: "prod-default-aaa", Provider: "DigitalOcean", Tag: "doks-cluster:prod"},
		{Name: "web-1", Provider: "DigitalOcean"},
		{Id: "222", Name: "byoc-default-zzz", Provider: "DigitalOcean", Tag: "doks-cluster:byoc"},
	}

	got := unionMachines(house, byoc)
	if len(got) != 3 {
		t.Fatalf("union want 3 deduped machines (111, 999/web-1, 222), got %d: %+v", len(got), got)
	}
	seen := map[string]*service.Machine{}
	for _, m := range got {
		seen[m.Id] = m
	}
	if _, ok := seen["111"]; !ok {
		t.Errorf("DOKS node 111 missing from union")
	}
	if _, ok := seen["222"]; !ok {
		t.Errorf("BYOC-only node 222 missing from union")
	}
	if _, ok := seen["999"]; !ok {
		t.Errorf("droplet 999/web-1 missing; name-collision dedup must not drop the house row")
	}

	// Empty sources are honest empties, never nil (JSON encodes []).
	if got := unionMachines(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("empty union must be non-nil empty slice, got %#v", got)
	}
}

// Nodes must put an ARRAY on the wire even when the org has none.
//
// It is the difference between "no nodes" and "this service does not serve this
// op", and a reader on the far side of a version skew has nothing else to tell
// them apart: a build that answers `{}` or `{"nodes":null}` decodes into a caller
// as an empty fleet and reports nothing wrong. cloud folds these nodes into the
// world fleet, so the wrong answer there is a silently smaller estate.
func TestNodesIsAlwaysAnArray(t *testing.T) {
	b, err := json.Marshal(&Nodes{Nodes: unionMachines(nil, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"nodes":[]}`; got != want {
		t.Fatalf("empty Nodes = %s, want %s", got, want)
	}
}
