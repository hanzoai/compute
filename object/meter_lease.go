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
	"time"
)

// MeterLease is a single-flight lease for the hourly compute-metering sweep.
// Its whole purpose is a MONEY-SAFETY invariant: the visor Deployment runs
// replicas: 2 with no leader election, so without a lease BOTH replicas would run
// service.MeterRunningMachines every hour and debit every running machine twice
// (the per-machine hour-bucketed RequestID is only a dedup HINT — commerce's
// RecordUsage does NOT dedup the withdraw transaction on requestId, so the client
// key alone does not stop a duplicate debit). The lease makes exactly one replica
// perform the sweep per wall-clock hour, cluster-wide.
//
// The mechanism is the same insert-once-wins primitive the rest of object uses:
// Hour is the PK, so only the FIRST Insert for a given hour succeeds; a concurrent
// replica's Insert fails the unique-key constraint and that replica skips the
// hour. It survives a mid-hour restart too — a restarted replica finds the hour
// already claimed and does not re-sweep (so a rollout cannot re-bill the hour).
type MeterLease struct {
	Hour        string `xorm:"varchar(12) notnull pk" json:"hour"` // UTC "YYYYMMDDHH" bucket — the billable unit.
	CreatedTime string `xorm:"varchar(100)" json:"createdTime"`
}

// meterLeaseTTL bounds how long claimed-hour rows are retained. One day is far
// longer than any sweep, so pruning never races an in-flight claim, and the table
// stays tiny.
const meterLeaseTTL = 24 * time.Hour

// ClaimMeterHour attempts to claim the hourly metering sweep for the wall-clock
// hour containing now, cluster-wide. It returns true to EXACTLY ONE caller per
// hour (the replica whose Insert wins the Hour primary key); every other replica
// — and any re-entry within the same hour after a restart — gets false and must
// skip the sweep.
//
// A DB error (unreachable/constraint) yields false: fail SAFE for a paid product
// means NOT sweeping (better a missed hour, reconciled later, than a duplicate
// debit). Best-effort prune of stale rows runs only for the winner so it costs
// nothing on the hot skip path.
func ClaimMeterHour(now time.Time) bool {
	hour := now.UTC().Format("2006010215")
	affected, err := adapter.engine.Insert(&MeterLease{
		Hour:        hour,
		CreatedTime: now.UTC().Format(time.RFC3339),
	})
	if err != nil || affected == 0 {
		return false // another replica already owns this hour (dup PK), or DB error -> fail safe (no sweep).
	}
	pruneMeterLeases(now.Add(-meterLeaseTTL))
	return true
}

// pruneMeterLeases deletes lease rows older than cutoff. Best-effort: a failure
// only leaves stale rows, never causes a double debit.
func pruneMeterLeases(cutoff time.Time) {
	_, _ = adapter.engine.Where("hour < ?", cutoff.UTC().Format("2006010215")).Delete(&MeterLease{})
}
