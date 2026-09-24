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

package object

import "testing"

// TestIsActiveCloudProvider pins the predicate: an active row of a cloud
// category, whatever label it carries — the account's key is in egress custody,
// so a row is never judged by whether it holds one.
func TestIsActiveCloudProvider(t *testing.T) {
	cases := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"Cloud", &Provider{Category: "Cloud", State: "Active"}, true},
		{"Public Cloud", &Provider{Category: "Public Cloud", State: "Active"}, true},
		{"Private Cloud", &Provider{Category: "Private Cloud", State: "Active"}, true},
		{"inactive state rejected", &Provider{Category: "Cloud", State: "Inactive"}, false},
		{"empty state rejected", &Provider{Category: "Cloud", State: ""}, false},
		{"blockchain category rejected", &Provider{Category: "Blockchain", State: "Active"}, false},
		{"unknown category rejected", &Provider{Category: "Storage", State: "Active"}, false},
	}
	for _, tc := range cases {
		if got := isActiveCloudProvider(tc.p); got != tc.want {
			t.Errorf("%s: isActiveCloudProvider = %v, want %v", tc.name, got, tc.want)
		}
	}
}
