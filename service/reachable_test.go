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

// reachable_test.go pins the difference between "a credential is SET" and "a
// credential WORKS" — the distinction the whole estate was missing when a revoked
// DigitalOcean token kept every presence check returning true.
package service

import (
	"context"
	"testing"
)

// houseToken points the house-token resolver at a value for one test. It sets the
// env var houseDOToken reads first, so it wins over app.conf without touching it.
func houseToken(t *testing.T, token string) {
	t.Helper()
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", token)
}

// TestARevokedTokenIsStillANonEmptyString is the mechanism finding, as a test.
//
// Every "is DO configured?" check in the estate is a PRESENCE check, and a revoked
// token is still a non-empty string — so ComputeConfigured stays true through a
// revocation, the code takes the configured branch, and fails inside it. That is
// why so much of this reported zeros that read as real data rather than "not
// connected". ComputeReachable is the question that actually distinguishes them.
//
// The context is cancelled before the call, so this is hermetic: no network, no
// DigitalOcean, no token minted. Cancellation can only produce an error if a call
// was actually ATTEMPTED, which is exactly the property under test — a presence
// check would sail through it, which is the point.
func TestARevokedTokenIsStillANonEmptyString(t *testing.T) {
	houseToken(t, "dop_v1_a_revoked_token_is_still_a_string")

	if !ComputeConfigured() {
		t.Fatal("a non-empty token must read as configured — that is the premise: presence cannot see a revocation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ComputeReachable(ctx); err == nil {
		t.Fatal("ComputeReachable answered without reaching the provider — it is a presence check wearing a reachability name, " +
			"and the hourly sweep would claim (and destroy) an hour it cannot bill")
	}
}

// TestNothingToAskIsNotAFailure keeps the two negative cases apart. An UNCONFIGURED
// house account is not an unreachable one: it means there are no house resources at
// all, so an empty answer is the TRUE answer and the hour must still be claimed —
// otherwise a deployment with no house token stops billing its tenants' own
// resources forever, which trades one revenue outage for another.
func TestNothingToAskIsNotAFailure(t *testing.T) {
	houseToken(t, "")

	if ComputeConfigured() {
		t.Skip("app.conf supplies a house token in this environment; the unconfigured case cannot be isolated here")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even cancelled: with nothing to ask, nothing is asked.
	if err := ComputeReachable(ctx); err != nil {
		t.Fatalf("an unconfigured house account reported unreachable (%v) — "+
			"'there is nothing to ask' and 'the answer did not come back' are different facts", err)
	}
}
