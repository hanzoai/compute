// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

import "testing"

// splitOwnerName is the panic-safety core of machineIdFromPath: the `:id` path
// param is attacker-controlled and must never crash the request. util's
// GetOwnerAndNameFromId panics on != 2 tokens and NoCheck index-panics on a
// single token; splitOwnerName must fail closed instead.
func TestSplitOwnerNameNeverPanics(t *testing.T) {
	cases := []struct {
		id        string
		wantOwner string
		wantName  string
	}{
		{"hanzoai/m1", "hanzoai", "m1"},
		{"m1", "", ""},          // bare name — the common :id shape; must NOT panic
		{"", "", ""},            // empty
		{"a/b/c", "", ""},       // three tokens
		{"/m1", "", ""},         // empty owner
		{"hanzoai/", "", ""},    // empty name
		{"/", "", ""},           // both empty
		{"hanzoai/m1/", "", ""}, // trailing slash → three tokens
	}
	for _, tc := range cases {
		// A panic here fails the test rather than the process.
		o, n := splitOwnerName(tc.id)
		if o != tc.wantOwner || n != tc.wantName {
			t.Errorf("splitOwnerName(%q) = (%q,%q), want (%q,%q)", tc.id, o, n, tc.wantOwner, tc.wantName)
		}
	}
}
