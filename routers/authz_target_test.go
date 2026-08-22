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

package routers

import "testing"

// THE ADDRESS NAMES THE TARGET, AND THE SEAM READS IT FROM THE ADDRESS.
//
// This is the fact the whole resource surface rests on. Authorization runs as
// middleware; middleware has no route parameters. A path-addressed resource
// whose seam reads c.Param therefore decides on an empty owner and REFUSES a
// caller the query spelling admitted — silently, because the handler never runs
// and its own tests still pass.
func TestPathTarget(t *testing.T) {
	for _, c := range []struct{ path, owner, name string }{
		// The grammar, in full.
		{"/v1/assets", "", ""},
		{"/v1/assets/acme", "acme", ""},
		{"/v1/assets/acme/db-1", "acme", "db-1"},
		{"/v1/volumes/acme/vol-1/attachment", "acme", "vol-1"},
		{"/v1/sessions/acme/s-9/state", "acme", "s-9"},
		// Not a target: no kind, or not this version.
		{"/v1", "", ""},
		{"/health", "", ""},
		{"/v2/assets/acme/db-1", "", ""},
		// Trailing slash is the same address.
		{"/v1/assets/acme/db-1/", "acme", "db-1"},
	} {
		o, n := pathTarget(c.path)
		if o != c.owner || n != c.name {
			t.Errorf("%s -> (%q,%q), want (%q,%q)", c.path, o, n, c.owner, c.name)
		}
	}
}
