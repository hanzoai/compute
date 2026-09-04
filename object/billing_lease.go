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

import (
	"github.com/hanzoai/compute/logs"
	"time"
)

// claimLease is the ONE exactly-once primitive behind every billing idempotency lease.
// It composes the TWO guarantees that make hourly metering exactly-once under the
// multi-replica compute Deployment, so there is exactly one such mechanism cluster-wide:
//
//	(1) single-writer gate  — only the HRW-elected owner of the `_global` coord DB may
//	    claim; every other replica returns false and skips (billingOwner, coordinator.go).
//	(2) durable lease history — before the insert, PULL the prior owner's committed lease
//	    rows into the live coord (a new owner after a leadership flip sees an already-taken
//	    hour/unit and its own insert fails the PK); after a winning insert, SHIP the coord
//	    SYNCHRONOUSLY before returning true, so the claim is durable before the caller
//	    debits. A missed hour is reconciled; a double debit is not — so every failure here
//	    (not owner, pull failed, insert lost, ship failed) returns false: FAIL CLOSED.
//
// Under Postgres the pull/ship are no-ops (the shared engine is already the single
// linearizable coordination store) and this reduces to the historical insert-once wins.
func claimLease(row interface{}) bool {
	// (1) Gate: only the single elected writer bills. A non-owner never claims, so it
	// never debits — the whole point of single-flight across replicas.
	if !billingOwner() {
		return false
	}
	// (2a) Hydrate: load the prior owner's committed leases so the insert-once PK holds
	// GLOBALLY, not per-pod. A hydrate error means we cannot prove we have the prior
	// owner's rows → fail closed (do not claim).
	if err := pullSharedLeases(); err != nil {
		logs.Warning("billing lease: hydrate coord before claim failed (skipping to avoid double debit): %v", err)
		return false
	}
	// Insert-once on the SHARED coord engine (the documented entry point for shared
	// tables): the FIRST insert for this PK wins; a concurrent replica's insert — or a
	// replay after a restart, or a row hydrated from the prior owner — loses.
	affected, err := Shared().Insert(row)
	if err != nil || affected != 1 {
		return false // lost the PK (already claimed) or DB error → fail closed.
	}
	// (2b) Ship SYNCHRONOUSLY: persist the claim to the object store BEFORE the caller
	// debits, so the next owner cannot re-claim this hour/unit. If the ship fails the
	// claim is not durable, so we must NOT let money move — fail closed (a missed debit
	// is reconciled). The local row stays inserted, so THIS replica remains idempotent.
	if err := pushShared(); err != nil {
		logs.Warning("billing lease: ship coord after claim failed (skipping debit; claim not durable): %v", err)
		return false
	}
	return true
}

// BillingLease is the generic single-flight lease for a billable UNIT — a
// daily BYOC-cost line ("byoc:<owner>:<provider>:<YYYYMMDD>") or a monthly
// per-device line ("device:<owner>:<worker>:<YYYYMM>"). It is the money-safety
// twin of MeterLease: compute runs replicas: 2 with no leader election and commerce
// does NOT dedup the withdraw on requestId, so without a cluster-wide claim BOTH
// replicas would meter the same unit and double-bill it.
//
// It is a SEPARATE table from MeterLease on purpose: the hourly compute sweep keeps
// using MeterLease unchanged, so a rolling deploy of this change can never make one
// replica claim an hour in `meter_lease` while another claims the same hour in a
// renamed column — which would double-bill every hour spanning the rollout. Unit is
// varchar(128) (MeterLease.Hour is varchar(12)); the daily/monthly keys do not fit
// the hour column, which is the other reason for a distinct table.
type BillingLease struct {
	Unit        string `xorm:"varchar(128) notnull pk" json:"unit"`
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
}

// billingLeaseTTL bounds how long claimed-unit rows are retained. 40 days is longer
// than the longest billing cadence (one month), so a unit row always outlives every
// legitimate re-run of its own unit (a day/month is billed exactly once and never
// re-billed after it closes), and the table stays small.
const billingLeaseTTL = 40 * 24 * time.Hour

// ClaimBillingUnit claims a billing unit cluster-wide for exactly-once metering. It
// returns true to EXACTLY ONE caller for a given unit; every other replica — and
// any replay of the same unit — gets false and must skip. Best-effort prune of
// stale rows runs only for the winner, so it costs nothing on the hot skip path.
func ClaimBillingUnit(unit string, now time.Time) bool {
	if !claimLease(&BillingLease{Unit: unit, CreatedTime: now.UTC().Format(time.RFC3339)}) {
		return false
	}
	pruneBillingLeases(now.Add(-billingLeaseTTL))
	return true
}

// pruneBillingLeases deletes lease rows older than cutoff. created_time is stored as
// fixed-width UTC RFC3339, so a lexical "<" compare is a chronological compare. Runs on
// the SHARED coord engine (where the leases live). Best-effort: a failure only leaves
// stale rows, never causes a double debit.
func pruneBillingLeases(cutoff time.Time) {
	_, _ = Shared().Where("created_time < ?", cutoff.UTC().Format(time.RFC3339)).Delete(&BillingLease{})
}
