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

package controllers

import (
	"testing"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// A machine's agent is another org's to touch only for a SuperAdmin: an org
// admin, or a member of the `built-in` org, reaches its own org's machines alone.
func TestAnOrgAdminReachesOnlyItsOwnMachines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		user  *iamsdk.User
		allow bool
	}{
		{"acting in admin", &iamsdk.User{Owner: "admin", Name: "z"}, true},
		{"an org admin of acme", &iamsdk.User{Owner: "acme", Name: "boss", IsAdmin: true}, false},
		{"the built-in org", &iamsdk.User{Owner: "built-in", Name: "admin"}, false},
		{"a member of beta", &iamsdk.User{Owner: "beta", Name: "b"}, true},
	} {
		if err := authorize(tc.user, "beta/drop-a"); (err == nil) != tc.allow {
			t.Errorf("%s on beta/drop-a: %v, want allowed = %v", tc.name, err, tc.allow)
		}
	}
}
