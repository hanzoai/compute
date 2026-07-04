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

import "time"

// claimLease is the ONE insert-once-wins primitive behind every billing
// idempotency lease: only the FIRST Insert for a given primary key succeeds; a
// concurrent replica's Insert — or a replay after a restart — hits the unique-key
// constraint and returns affected==0. A DB error also yields false: for a paid
// product the safe failure is NOT to bill (a missed unit is reconciled later,
// whereas a double Insert-that-looked-like-a-win would be a duplicate debit). Both
// MeterLease (hourly compute sweep) and BillingLease (daily/monthly fleet units)
// are built on it, so there is exactly one exactly-once mechanism.
func claimLease(row interface{}) bool {
	affected, err := adapter.engine.Insert(row)
	return err == nil && affected == 1
}

// BillingLease is the generic single-flight lease for a billable UNIT — a
// daily BYOC-cost line ("byoc:<owner>:<provider>:<YYYYMMDD>") or a monthly
// per-device line ("device:<owner>:<worker>:<YYYYMM>"). It is the money-safety
// twin of MeterLease: visor runs replicas: 2 with no leader election and commerce
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
// fixed-width UTC RFC3339, so a lexical "<" compare is a chronological compare.
// Best-effort: a failure only leaves stale rows, never causes a double debit.
func pruneBillingLeases(cutoff time.Time) {
	_, _ = adapter.engine.Where("created_time < ?", cutoff.UTC().Format(time.RFC3339)).Delete(&BillingLease{})
}
