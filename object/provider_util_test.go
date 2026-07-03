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

// TestIsActiveCloudProvider pins the widened predicate: the live prod DO row
// carries its token in ClientId (ClientSecret empty) with Category "Cloud", and
// must be accepted alongside the legacy Public/Private Cloud shapes.
func TestIsActiveCloudProvider(t *testing.T) {
	cases := []struct {
		name string
		p    *Provider
		want bool
	}{
		{"prod DO row: token in ClientId, Category Cloud", &Provider{ClientId: "dop_v1_token", Category: "Cloud", State: "Active"}, true},
		{"token in ClientSecret, Public Cloud", &Provider{ClientSecret: "secret", Category: "Public Cloud", State: "Active"}, true},
		{"token in ClientId, Private Cloud", &Provider{ClientId: "token", Category: "Private Cloud", State: "Active"}, true},
		{"both tokens set, Cloud", &Provider{ClientId: "a", ClientSecret: "b", Category: "Cloud", State: "Active"}, true},
		{"no token rejected", &Provider{Category: "Cloud", State: "Active"}, false},
		{"inactive state rejected", &Provider{ClientId: "token", Category: "Cloud", State: "Inactive"}, false},
		{"empty state rejected", &Provider{ClientId: "token", Category: "Cloud", State: ""}, false},
		{"blockchain category rejected", &Provider{ClientId: "token", Category: "Blockchain", State: "Active"}, false},
		{"unknown category rejected", &Provider{ClientId: "token", Category: "Storage", State: "Active"}, false},
	}
	for _, tc := range cases {
		if got := isActiveCloudProvider(tc.p); got != tc.want {
			t.Errorf("%s: isActiveCloudProvider = %v, want %v", tc.name, got, tc.want)
		}
	}
}
