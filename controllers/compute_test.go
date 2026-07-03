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
	byName := filterMachines(all, "", "crawler")
	if len(byName) != 3 {
		t.Fatalf("?name=crawler = %d machines, want 3", len(byName))
	}
	for i, m := range byName {
		if w := batchMemberName("crawler", i); m.DisplayName != w {
			t.Errorf("member %d = %q, want %q", i, m.DisplayName, w)
		}
	}

	// ?kind=bot selects the 3 bots and excludes the plain machine.
	if bots := filterMachines(all, service.KindBot, ""); len(bots) != 3 {
		t.Fatalf("?kind=bot = %d machines, want 3", len(bots))
	}
	if machines := filterMachines(all, service.KindMachine, ""); len(machines) != 1 || machines[0].DisplayName != "db-1" {
		t.Fatalf("?kind=machine = %v, want [db-1]", machines)
	}

	// No filter passes everything (identity).
	if len(filterMachines(all, "", "")) != len(all) {
		t.Fatalf("empty filter changed the list")
	}

	// Delete one member (scale down = delete): the ?name= group drops to 2.
	remaining := []*service.Machine{all[0], all[2], all[3]} // crawler-001 destroyed
	got := filterMachines(remaining, "", "crawler")
	if len(got) != 2 || got[0].DisplayName != "crawler-000" || got[1].DisplayName != "crawler-002" {
		t.Fatalf("after delete ?name=crawler = %v, want [crawler-000 crawler-002]", got)
	}
}
