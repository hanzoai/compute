// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

package object

import (
	"testing"

	"github.com/hanzoai/compute/service"
)

// Billed hours are kept on the shared store: a mark moves forward only, reports
// which moved, and survives the pod restarting on the same disk.
func TestTheMeterLedgerMovesForwardOnlyAndIsKept(t *testing.T) {
	root := t.TempDir()
	activate(t, newReplicaStore(t, root))
	l := meterLedger{}
	if got, err := l.Through("m-00000000000000000001"); err != nil || got.Hour != "" {
		t.Fatalf("an unbilled machine reads %+v, %v", got, err)
	}
	moved, err := l.Advance(map[string]service.Mark{"m-00000000000000000001": {Hour: "2026070215"}, "m-00000000000000000002": {Hour: "2026070215"}})
	if err != nil || !moved["m-00000000000000000001"] || !moved["m-00000000000000000002"] {
		t.Fatalf("first advance moved %v, %v", moved, err)
	}
	moved, err = l.Advance(map[string]service.Mark{"m-00000000000000000001": {Hour: "2026070215"}, "m-00000000000000000002": {Hour: "2026070214"}})
	if err != nil || len(moved) != 0 {
		t.Fatalf("an advance to a billed hour, or backwards, moved %v, %v", moved, err)
	}
	moved, _ = l.Advance(map[string]service.Mark{"m-00000000000000000001": {Hour: "2026070217", Carry: 12345}})
	if !moved["m-00000000000000000001"] {
		t.Fatal("a later hour did not move the mark")
	}

	// A restart: the same disk, a new store.
	_ = store.Close()
	activate(t, newReplicaStore(t, root))
	for machine, want := range map[string]service.Mark{"m-00000000000000000001": {Hour: "2026070217", Carry: 12345}, "m-00000000000000000002": {Hour: "2026070215"}} {
		if got, err := l.Through(machine); err != nil || got != want {
			t.Errorf("after a restart %s is billed through %+v (%v), want %+v", machine, got, err, want)
		}
	}
	if moved, _ := l.Advance(map[string]service.Mark{"m-00000000000000000001": {Hour: "2026070217"}}); len(moved) != 0 {
		t.Fatal("a restart forgot an hour already billed")
	}
}
