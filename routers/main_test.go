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

package routers

import (
	"os"
	"testing"

	"github.com/hanzoai/visor/authz"
)

// TestMain builds the authorizer the way the server does, because without it
// these tests were not measuring authorization at all.
//
// authz.Enforcer is a package variable and InitAuthz is called from exactly one
// place — pkg/visor/embed.go, the real bootstrap. A test that routes a request
// through ApiFilter without it reaches Enforcer.Enforce on a NIL enforcer, which
// panics, which Recover turns into a 500. A "this route is gated" assertion
// written as "not 200" then passes on the strength of a nil dereference, and
// would go on passing with the policy deleted.
//
// The policy is a static string and the enforcer is in memory — no store, no
// network — so building the real one costs nothing and makes a 403 mean 403.
func TestMain(m *testing.M) {
	authz.InitAuthz()
	os.Exit(m.Run())
}
