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

// These are the MONEY tests: they prove the billing leases are exactly-once under a
// multi-replica Deployment. They exercise the real claim path (billingOwner gate +
// hydrate + insert-once + ship) against the file/temp Store the package already uses,
// with membership injected so ownership is deterministic without a cluster.

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/ha"
)

// stubMembership forces a fixed election result so a test can BE the owner or NOT be,
// with no Kubernetes API. It is the injectable seam billingOwner elects over.
type stubMembership struct {
	id   string
	set  []ha.Member
	fail bool // simulate an unreadable membership set (fail-closed path).
}

func (s stubMembership) Self() string { return s.id }
func (s stubMembership) Members(context.Context) ([]ha.Member, error) {
	if s.fail {
		return nil, os.ErrPermission
	}
	return s.set, nil
}

// withMembership swaps the process membership source for the duration of a test and
// restores it on cleanup, so the tests compose with the rest of the package.
func withMembership(t *testing.T, m ha.Membership) {
	t.Helper()
	prev := membership
	membership = m
	t.Cleanup(func() { membership = prev })
}

// ownerOf returns the member ID HRW elects for the coord key over set — used to pick a
// self that IS (or is NOT) the owner deterministically.
func electedOwner(set []ha.Member) string {
	o, _ := ha.Owner(coordKey, set)
	return o.ID
}

// newReplicaStore opens a Base store rooted at a specific dir — modelling ONE pod with
// its own local root, sharing whatever REPLICA_STORE the caller set. It does NOT touch
// the process singletons (store/adapter): use activate to make a pod "the one executing".
// Registers a close on cleanup and restores the process singletons after the test.
func newReplicaStore(t *testing.T, root string) *baseStore {
	t.Helper()
	bs, err := newBaseStore(root)
	if err != nil {
		t.Fatalf("newBaseStore(%s): %v", root, err)
	}
	prevStore, prevAdapter := store, adapter
	t.Cleanup(func() {
		_ = bs.Close()
		store, adapter = prevStore, prevAdapter
	})
	return bs
}

// activate points the process store + adapter at bs — the equivalent of "this pod is now
// the one handling the request/tick". Switching between two replicas' stores in one test
// models two pods that share an object store but have independent local coord files.
func activate(t *testing.T, bs *baseStore) {
	t.Helper()
	store = bs
	adapter = &Adapter{driverName: "sqlite", engine: bs.Shared()}
}

// --- (c) non-owner replica does not meter -----------------------------------------

// TestNonOwnerDoesNotClaim proves a replica that is NOT the elected owner never claims
// a lease — it skips cleanly (returns false), so it never debits. This is the gate that
// makes the sweep single-flight across replicas.
func TestNonOwnerDoesNotClaim(t *testing.T) {
	installBaseStore(t)

	set := []ha.Member{{ID: "compute-a"}, {ID: "compute-b"}}
	owner := electedOwner(set)
	// Pick self as the NON-owner of the pair.
	self := "compute-a"
	if self == owner {
		self = "compute-b"
	}
	withMembership(t, stubMembership{id: self, set: set})

	now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
	if ClaimMeterHour(now) {
		t.Fatal("non-owner must NOT win ClaimMeterHour (would double-debit)")
	}
	if ClaimBillingUnit("device:org-a:box:202607", now) {
		t.Fatal("non-owner must NOT win ClaimBillingUnit (would double-bill)")
	}

	// And the OWNER, on the same store, DOES win — proving the gate admits exactly the
	// elected writer.
	withMembership(t, stubMembership{id: owner, set: set})
	if !ClaimMeterHour(now) {
		t.Fatal("elected owner must win ClaimMeterHour")
	}
}

// TestUnknownMembershipFailsClosed proves an in-cluster replica that cannot read its
// peer set does NOT claim — a paid product never bills under uncertainty.
func TestUnknownMembershipFailsClosed(t *testing.T) {
	installBaseStore(t)
	withMembership(t, stubMembership{id: "compute-a", fail: true})

	now := time.Date(2026, 7, 5, 11, 0, 0, 0, time.UTC)
	if ClaimMeterHour(now) {
		t.Fatal("claim must fail closed when membership is unreadable")
	}
	if ClaimBillingUnit("byoc:org:prov:20260705", now) {
		t.Fatal("billing-unit claim must fail closed when membership is unreadable")
	}
}

// --- replicas: 1 elects itself, and N elects exactly one --------------------------

// TestSingleReplicaAlwaysOwner proves the CTO invariant: at replicas: 1 the sole pod
// always elects itself owner with NO external coordinator, so it bills normally.
func TestSingleReplicaAlwaysOwner(t *testing.T) {
	installBaseStore(t)
	solo := []ha.Member{{ID: "compute-only"}}
	withMembership(t, stubMembership{id: "compute-only", set: solo})

	if !billingOwner() {
		t.Fatal("a single replica must always be its own billing owner")
	}
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	if !ClaimMeterHour(now) {
		t.Fatal("single replica must claim the hour and bill normally")
	}
}

// TestExactlyOneOwnerAcrossReplicas proves that for a fixed membership set, exactly ONE
// replica is elected owner — the property that guarantees only one biller at replicas: N.
func TestExactlyOneOwnerAcrossReplicas(t *testing.T) {
	for _, n := range []int{1, 2, 3, 5} {
		set := make([]ha.Member, n)
		for i := 0; i < n; i++ {
			set[i] = ha.Member{ID: "compute-" + string(rune('a'+i))}
		}
		owners := 0
		for _, m := range set {
			if ha.IsOwner(coordKey, m.ID, set) {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("replicas=%d: elected %d owners, want exactly 1", n, owners)
		}
	}
}

// TestNoMembershipSourceFailsClosed proves the DEFAULT — no source registered, which is
// what a build linking no cluster client gets — refuses to bill rather than assuming it
// is alone. Assuming "I am the only replica" is the one guess that double-debits.
func TestNoMembershipSourceFailsClosed(t *testing.T) {
	withMembership(t, nil)
	if billingOwner() {
		t.Fatal("with no membership source registered, a replica must NOT elect itself billing owner")
	}
}

// --- (a) concurrent double-insert → exactly one wins ------------------------------

// TestConcurrentClaimMeterHourExactlyOnce races many goroutines on the SAME hour, all
// as the elected owner, and asserts EXACTLY ONE wins — the insert-once PK serialized on
// the coord engine. This is the direct money-safety proof for the hourly sweep.
func TestConcurrentClaimMeterHourExactlyOnce(t *testing.T) {
	installBaseStore(t)
	solo := []ha.Member{{ID: "compute-only"}}
	withMembership(t, stubMembership{id: "compute-only", set: solo})

	now := time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC)
	const racers = 16
	var wins int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ClaimMeterHour(now) {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("concurrent ClaimMeterHour for one hour won %d times, want exactly 1 (double-debit guard)", wins)
	}
}

// TestConcurrentClaimBillingUnitExactlyOnce is the same proof for the fleet billing
// unit (daily BYOC / monthly device): concurrent claims of one unit → exactly one wins.
func TestConcurrentClaimBillingUnitExactlyOnce(t *testing.T) {
	installBaseStore(t)
	solo := []ha.Member{{ID: "compute-only"}}
	withMembership(t, stubMembership{id: "compute-only", set: solo})

	now := time.Date(2026, 7, 5, 14, 0, 0, 0, time.UTC)
	unit := "device:org-a:box-1:202607"
	const racers = 16
	var wins int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if ClaimBillingUnit(unit, now) {
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("concurrent ClaimBillingUnit for one unit won %d times, want exactly 1 (double-bill guard)", wins)
	}
}

// --- (b) leadership handoff → new owner does NOT re-claim --------------------------

// TestLeadershipHandoffNoReclaim is the failover proof. Replica O claims hour H and
// ships its coord to a SHARED object store. Ownership flips to a FRESH replica N (its own
// empty local root) pointed at the SAME object store. N hydrates O's lease rows before it
// can claim, so N's claim of hour H FAILS the (now globally-visible) PK — no re-bill.
//
// This is the guarantee the single-writer gate alone cannot provide: across a leadership
// flip, the durable lease history makes the insert-once PK hold cluster-wide.
func TestLeadershipHandoffNoReclaim(t *testing.T) {
	// One shared object store both replicas ship/hydrate through (file:// backend).
	shared := t.TempDir()
	t.Setenv("REPLICA_STORE", "file://"+shared)

	hourH := time.Date(2026, 7, 5, 15, 0, 0, 0, time.UTC)
	hourKey := hourH.UTC().Format("2006010215")
	set := []ha.Member{{ID: "compute-old"}, {ID: "compute-new"}}

	// Two LIVE replicas, each its own local root, SHARING one object store. We model
	// "which pod is executing" by pointing the process store/adapter at that pod's engine
	// — exactly what a request/tick does inside each pod.
	bsO := newReplicaStore(t, t.TempDir())
	bsN := newReplicaStore(t, t.TempDir()) // N boots NOW, while the store is empty — its
	// boot-hydrate pulls nothing, so N's coord is genuinely STALE for anything O claims
	// later. This is the dangerous case boot-hydrate alone cannot cover.

	// --- Replica O becomes owner and claims + ships hour H. ---
	activate(t, bsO)
	withMembership(t, stubMembership{id: "compute-old", set: []ha.Member{{ID: "compute-old"}}})
	if !ClaimMeterHour(hourH) {
		t.Fatal("replica O (owner) must claim hour H")
	}

	// --- Ownership flips to N, which has been running with a STALE coord. ---
	activate(t, bsN)
	// Prove N's coord is genuinely stale for H (boot-hydrate ran before O's claim): the
	// ONLY thing that can save N is the claim-time hydrate.
	staleEmpty, err := bsN.Shared().Exist(&MeterLease{Hour: hourKey})
	if err != nil {
		t.Fatalf("N stale-coord check: %v", err)
	}
	if staleEmpty {
		t.Fatal("precondition failed: N's coord already has H before claim-time hydrate")
	}
	withMembership(t, stubMembership{id: "compute-new", set: set})

	// N attempts to claim the SAME hour H. claimLease hydrates O's shipped lease rows
	// FIRST, so the insert-once PK now sees H taken → N LOSES (no re-bill across failover).
	if ClaimMeterHour(hourH) {
		t.Fatal("leadership handoff: new owner RE-CLAIMED hour H — double-debit across failover")
	}
	// N is still a live owner: it can claim a different, unclaimed hour.
	if !ClaimMeterHour(hourH.Add(time.Hour)) {
		t.Fatal("new owner must be able to claim the next, unclaimed hour")
	}
}

// TestHandoffBillingUnitNoReclaim is the same failover proof for a fleet billing unit:
// a unit billed by O and shipped is NOT re-billed by N after N hydrates.
func TestHandoffBillingUnitNoReclaim(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("REPLICA_STORE", "file://"+shared)

	now := time.Date(2026, 7, 5, 16, 0, 0, 0, time.UTC)
	unit := "byoc:org-x:aws-1:20260705"
	set := []ha.Member{{ID: "compute-old"}, {ID: "compute-new"}}

	bsO := newReplicaStore(t, t.TempDir())
	bsN := newReplicaStore(t, t.TempDir()) // boots empty (stale for anything O bills later).

	activate(t, bsO)
	withMembership(t, stubMembership{id: "compute-old", set: []ha.Member{{ID: "compute-old"}}})
	if !ClaimBillingUnit(unit, now) {
		t.Fatal("replica O (owner) must claim the billing unit")
	}

	activate(t, bsN)
	withMembership(t, stubMembership{id: "compute-new", set: set})
	if ClaimBillingUnit(unit, now) {
		t.Fatal("leadership handoff: new owner RE-BILLED the unit — double-bill across failover")
	}
}
