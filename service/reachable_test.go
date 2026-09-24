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

// reachable_test.go pins the difference between "the account is CONFIGURED" and
// "the account ANSWERS" — the question the hourly sweep asks before it spends an
// hour.
package service

import (
	"context"
	"testing"
)

// Presence cannot see a revocation: a configured region reads as configured
// whether or not the account still answers. ComputeReachable spends a real
// round trip through egress, so an account EC2 refuses is an unreachable one.
func TestPresenceCannotSeeARevocation(t *testing.T) {
	f := hostedFake(t)

	if err := ComputeReachable(context.Background()); err != nil {
		t.Fatalf("a working account reported unreachable: %v", err)
	}
	if n := len(f.Calls("DescribeInstances")); n != 1 {
		t.Fatalf("reachability made %d calls, want exactly one", n)
	}

	f.Refuse("DescribeInstances", "UnauthorizedOperation")
	if !ComputeConfigured() {
		t.Fatal("a configured region must read as configured — that is the premise")
	}
	if err := ComputeReachable(context.Background()); err == nil {
		t.Fatal("ComputeReachable answered without reaching the account — the hourly sweep would " +
			"claim (and destroy) an hour it cannot bill")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ComputeReachable(ctx); err == nil {
		t.Fatal("a call that never happened was reported as an answer")
	}
}

// An UNCONFIGURED account is not an unreachable one: there are no hosted
// machines, so an empty answer is the TRUE answer and the hour must still be
// claimed — otherwise a deployment with no hosted account stops billing its
// tenants' own resources forever.
func TestNothingToAskIsNotAFailure(t *testing.T) {
	f := hostedFake(t)
	t.Setenv(keyRegion, "")

	if ComputeConfigured() {
		t.Fatal("no region must read as unconfigured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // even cancelled: with nothing to ask, nothing is asked.
	if err := ComputeReachable(ctx); err != nil {
		t.Fatalf("an unconfigured account reported unreachable (%v) — "+
			"'there is nothing to ask' and 'the answer did not come back' are different facts", err)
	}
	if n := len(f.Calls("")); n != 0 {
		t.Fatalf("an unconfigured account was asked %d times", n)
	}
}
