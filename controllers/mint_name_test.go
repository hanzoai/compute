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

package controllers

import (
	"regexp"
	"strings"
	"testing"
)

// A machine launched with no name reached DigitalOcean with an empty one and came
// back 422 "Droplet must have a name". The batch path refused it here; the single
// path did not, so Tabs — which opens a scratch terminal and has no name to give —
// failed on every click.
func TestMintMachineNameIsAlwaysUsable(t *testing.T) {
	// A droplet name is a hostname: lowercase alphanumerics and dashes.
	ok := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

	for _, kind := range []string{"", "tab", "bot", "MACHINE", "we_ird kind/!"} {
		got := mintMachineName(kind)
		if got == "" {
			t.Fatalf("kind %q minted an empty name, which is the bug", kind)
		}
		if !ok.MatchString(got) {
			t.Errorf("kind %q minted %q, which a hostname cannot carry", kind, got)
		}
	}

	// The kind leads, so a name says what the machine is before it says which one.
	if !strings.HasPrefix(mintMachineName("tab"), "tab-") {
		t.Error("a tab should be named for what it is")
	}
	if !strings.HasPrefix(mintMachineName(""), "machine-") {
		t.Error("an unstated kind should still say something")
	}

	// Two clicks in the same second must not collide.
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		n := mintMachineName("tab")
		if seen[n] {
			t.Fatalf("minted %q twice in 500 tries", n)
		}
		seen[n] = true
	}
}
