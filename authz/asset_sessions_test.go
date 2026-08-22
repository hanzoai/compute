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

package authz

import "testing"

// The demo-mode door is ONE address. A suffix match would open the collection
// too, which creates a session against any asset — a different permission.
func TestAssetSessionsIsOneAddress(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/assets/acme/db-1/sessions": true,
		"/v1/sessions":                  false,
		"/v1/assets/acme/sessions":      false,
		"/v1/plans/acme/pro/sessions":   false,
		"/v1/assets/acme/db-1/tunnel":   false,
	} {
		if got := isAssetSessions(path); got != want {
			t.Errorf("isAssetSessions(%q) = %v, want %v", path, got, want)
		}
	}
}
