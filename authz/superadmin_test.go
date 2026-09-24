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

package authz

import (
	"testing"

	"github.com/hanzoai/iamsdk/v2/iamsdk"
)

// The one platform privilege is acting in the reserved admin org. An org admin
// is self-service in its own org; the `built-in` org is nobody's.
func TestOnlyTheAdminOrgIsPlatformPrivileged(t *testing.T) {
	InitAuthz()
	for _, tc := range []struct {
		name  string
		user  *iamsdk.User
		allow bool
	}{
		{"a member acting in admin", &iamsdk.User{Owner: "admin", Name: "z"}, true},
		{"a deleted member of admin", &iamsdk.User{Owner: "admin", Name: "z", IsDeleted: true}, false},
		{"the built-in org", &iamsdk.User{Owner: "built-in", Name: "admin"}, false},
		{"an org admin of acme", &iamsdk.User{Owner: "acme", Name: "boss", IsAdmin: true}, false},
	} {
		got := IsAllowed(tc.user, tc.user.Owner, tc.user.Name, "PUT", "/v1/providers/beta/aws", "beta", "aws")
		if got != tc.allow {
			t.Errorf("%s writing beta/aws: allowed = %v, want %v", tc.name, got, tc.allow)
		}
	}
}
