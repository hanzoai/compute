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

package main

import (
	"os"
	"strings"
	"testing"
)

// retiredModules are module paths this service must never link again, each with
// the reason. They are ARCHIVED on GitHub: a repo that cannot take a push cannot
// take a security fix, so no deployed binary may depend on one — the dependency
// is unpatchable by construction, whatever the code inside says.
//
// github.com/hanzoai/iam-v1 is the Casdoor lineage's old home. Its ROOT package
// was a second copy of the Casdoor Go SDK, 67 of 73 files byte-identical to
// github.com/hanzoai/iamsdk/v2/iamsdk, which is live and tagged. visor required
// it DIRECTLY while also linking the v2 lineage through cloud — one binary, both
// implementations. The SDK is the one that stays.
var retiredModules = map[string]string{
	"github.com/hanzoai/iam-v1": "archived Casdoor fork — use github.com/hanzoai/iamsdk/v2/iamsdk",
}

// retiredIn reports every retired module path named anywhere in a go.mod. It
// reads go.mod rather than the import graph on purpose: an import scan only
// catches a direct `import`, and the way an archived module comes back is
// transitively, through a dependency's own require. go.mod carries both.
func retiredIn(goMod string) []string {
	var found []string
	for _, line := range strings.Split(goMod, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line, "// indirect") {
			line = line[:i]
		}
		for _, f := range strings.Fields(line) {
			if _, retired := retiredModules[f]; retired {
				found = append(found, f)
			}
		}
	}
	return found
}

// TestRetiredInDetects proves the detector actually fires — a guard that cannot
// fail is not a guard. The fixture is the require line this repo carried until
// the port off the archived fork.
func TestRetiredInDetects(t *testing.T) {
	const fixture = "require (\n\tgithub.com/hanzoai/iam-v1 v1.31.36\n)\n"
	got := retiredIn(fixture)
	if len(got) != 1 || got[0] != "github.com/hanzoai/iam-v1" {
		t.Fatalf("retiredIn(fixture) = %v, want [github.com/hanzoai/iam-v1]", got)
	}
	// An indirect require is just as load-bearing at link time.
	if got := retiredIn("\tgithub.com/hanzoai/iam-v1 v1.31.36 // indirect\n"); len(got) != 1 {
		t.Errorf("retiredIn(indirect) = %v, want the path reported", got)
	}
	if got := retiredIn("\tgithub.com/hanzoai/iamsdk/v2 v2.2.1\n"); len(got) != 0 {
		t.Errorf("retiredIn(replacement) = %v, want none", got)
	}
}

// TestNoRetiredModules is the standing gate on the real manifest.
func TestNoRetiredModules(t *testing.T) {
	b, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, path := range retiredIn(string(b)) {
		t.Errorf("go.mod requires %s — %s", path, retiredModules[path])
	}
}
