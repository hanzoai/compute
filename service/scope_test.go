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

package service

import (
	"strings"
	"testing"
	"time"
)

// joinTags mirrors getMachineFromDroplet's tag read-back build: the DO tags a
// launch emits are joined into the comma-terminated Machine.Tag string EmitCompute
// later parses. This is the exact launch -> droplet tag -> read-back chain.
func joinTags(tags []string) string {
	joined := ""
	for _, t := range tags {
		joined += t + ","
	}
	return joined
}

// TestSetScopeRoundTripToComputeUsage proves the whole org > app > project chain:
// a launch that sets a scope (SetScope) lands hanzo-app/hanzo-project as droplet
// tags (buildDropletTags), the tags survive the read-back (Machine.Tag), and
// EmitCompute records app/project on the compute_usage row — the columns that were
// empty (0/0) before launch injected them.
func TestSetScopeRoundTripToComputeUsage(t *testing.T) {
	got := captureDatastore(t)

	spec := &CreateMachineSpec{Tags: map[string]string{orgTagKey: "acme"}}
	SetScope(spec, "web", "api")

	tags := buildDropletTags(spec)
	joined := joinTags(tags)
	if tagValue(joined, appTagKey) != "web" || tagValue(joined, projectTagKey) != "api" {
		t.Fatalf("scope tags not emitted onto the droplet: %v", tags)
	}

	m := &Machine{Id: "77", Size: "s-1vcpu-1gb", Tag: joined}
	EmitCompute("acme", ComputeLaunched, m, 5)
	select {
	case c := <-got:
		if c.row.Org != "acme" || c.row.App != "web" || c.row.Project != "api" {
			t.Fatalf("compute_usage scope = org=%q app=%q project=%q, want acme/web/api", c.row.Org, c.row.App, c.row.Project)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// TestNoScopeLeavesProjectEmpty proves the optional-scope invariant: a launch that
// omits app/project neither emits the tags nor breaks org attribution — the row
// carries org and leaves app/project empty, exactly as before the feature.
func TestNoScopeLeavesProjectEmpty(t *testing.T) {
	got := captureDatastore(t)

	spec := &CreateMachineSpec{Tags: map[string]string{orgTagKey: "acme"}}
	SetScope(spec, "", "") // omitted on this launch

	tags := buildDropletTags(spec)
	for _, tg := range tags {
		if strings.HasPrefix(tg, appTagKey+":") || strings.HasPrefix(tg, projectTagKey+":") {
			t.Fatalf("omitted scope must emit no app/project tag, got %q", tg)
		}
	}

	m := &Machine{Id: "78", Size: "s-1vcpu-1gb", Tag: joinTags(tags)}
	EmitCompute("acme", ComputeLaunched, m, 5)
	select {
	case c := <-got:
		if c.row.App != "" || c.row.Project != "" {
			t.Fatalf("omitted scope must stay empty, got app=%q project=%q", c.row.App, c.row.Project)
		}
		if c.row.Org != "acme" {
			t.Fatalf("org attribution must still work with no scope, got %q", c.row.Org)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no datastore insert received")
	}
}

// TestSetScopeRejectsUnsafeValue proves scope injection reuses org's safeTagField
// guard: a value carrying the tag read-back's "," / ":" separators is dropped and
// never reaches the spec, so it can fabricate neither a tag boundary nor a fake
// key:value on read-back.
func TestSetScopeRejectsUnsafeValue(t *testing.T) {
	spec := &CreateMachineSpec{Tags: map[string]string{orgTagKey: "acme"}}
	SetScope(spec, "ev,il", "ok:nope")
	if _, ok := spec.Tags[appTagKey]; ok {
		t.Fatalf("unsafe app value must not be set on the spec, tags=%v", spec.Tags)
	}
	if _, ok := spec.Tags[projectTagKey]; ok {
		t.Fatalf("unsafe project value must not be set on the spec, tags=%v", spec.Tags)
	}
}

// TestMachineScopeReadBack proves MachineApp/MachineProject are the read-back
// counterparts of SetScope (the ?project= list filter reads through them), empty
// when the machine carries no such tag.
func TestMachineScopeReadBack(t *testing.T) {
	m := &Machine{Tag: "hanzo-org:acme,hanzo-app:web,hanzo-project:api,"}
	if MachineApp(m) != "web" || MachineProject(m) != "api" {
		t.Fatalf("scope read-back = app=%q project=%q, want web/api", MachineApp(m), MachineProject(m))
	}
	bare := &Machine{Tag: "hanzo-org:acme,"}
	if MachineApp(bare) != "" || MachineProject(bare) != "" {
		t.Fatalf("scopeless machine must read empty, got app=%q project=%q", MachineApp(bare), MachineProject(bare))
	}
}
