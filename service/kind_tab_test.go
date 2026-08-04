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
		{KindTab, true},   // the reason the kind exists
		{KindBot, true},   // a bot is a tab that also runs the agent
		{KindMachine, false}, // bare compute publishes nothing
		{KindCluster, false},
		{KindFunction, false},
		{"", false},          // unknown falls back to machine
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
