// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

import "testing"

// A tab is a machine you can OPEN. The kinds nest — machine ⊂ tab ⊂ bot — so a
// terminal appears at tab and everything richer keeps it. Writing it as a
// threshold rather than a set is what makes a bot openable in Tabs for free;
// before there was no way to look inside one you had launched.
func TestKindTab_OpensIsAThresholdNotASet(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want bool
	}{
		{KindTab, true},      // the reason the kind exists
		{KindBot, true},      // a bot is a tab that also runs the agent
		{KindMachine, false}, // bare compute publishes nothing
		{KindCluster, false},
		{KindFunction, false},
		{"", false}, // unknown falls back to machine
		{"garbage", false},
	} {
		if got := Opens(tc.kind); got != tc.want {
			t.Errorf("Opens(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// tab has to survive the tag round trip, or a launch records it and every
// read-back — emit, sweep, destroy — silently sees `machine` instead.
func TestKindTab_SurvivesCanonicalisationAndTagging(t *testing.T) {
	if got := CanonicalKind("tab"); got != KindTab {
		t.Fatalf("CanonicalKind(tab) = %q — an unknown kind falls back to machine, so tab would never reach a droplet", got)
	}
	if got := CanonicalKind("  tab  "); got != KindTab {
		t.Errorf("CanonicalKind trims, so padded input must still be tab, got %q", got)
	}
	spec := &CreateMachineSpec{}
	SetKind(spec, KindTab)
	if got := spec.Tags[kindTagKey]; got != KindTab {
		t.Errorf("SetKind wrote %q to the kind tag, want %q", got, KindTab)
	}
}
