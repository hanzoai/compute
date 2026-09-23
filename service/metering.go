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

// metering.go is the ONE commerce metering path for resell compute. Both the
// launch debit (controllers/compute.go) and the recurring hourly debit
// (MeterRunningMachines, driven by task/ticker) price with PriceToCents and debit
// through RecordCompute on cloud's commerce API (commerce.go) — there is no
// second metering path for /v1 machines. Fleet billing (billing/) debits through
// the same client.
package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/telemetry"
)

// meteringProvider labels resell-compute usage in the commerce ledger so spend
// is attributable to this surface (product), independent of the DO size recorded
// as the Model.
const meteringProvider = "compute"

// PriceToCents converts a USD price to whole cents for billing. It Ceils (a paid
// product never under-charges) but subtracts a 1e-9 epsilon first so float64
// overshoot on a whole-cent price (0.07*100 = 7.00000000000000089) does not round
// up to 8. A true sub-cent price still ceils to >= 1; a $0 price yields 0 (free,
// no charge). This is the ONE price→cents rule shared by launch and recurring
// metering.
func PriceToCents(price float64) int64 {
	if price <= 0 {
		return 0
	}
	return int64(math.Ceil(price*100 - 1e-9))
}

// HourStamp is the per-hour bucket in the usage id: the same running resource
// metered twice within one wall-clock hour carries the SAME id, and cloud debits
// one id once. The single-flight lease in the ticker (object.ClaimMeterHour) keeps
// a second replica from sweeping at all; the id is what makes a retried debit
// harmless.
func HourStamp(now time.Time) string { return now.UTC().Format("2006010215") }

// CreatedInHour reports whether an RFC3339 create time falls in the same UTC
// hour bucket as stamp ("YYYYMMDDHH"). An empty or unparseable time is NOT in the
// hour (metered normally) — we only ever SKIP a resource we can prove the
// provision path already billed this hour.
//
// Exported because it is the ONE launch-hour rule, and every provision path that
// debits its first hour up front (machines, node pools) needs its recurring sweep
// to honour the same rule or that hour is billed twice.
func CreatedInHour(createdTime, stamp string) bool {
	if createdTime == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, createdTime)
	if err != nil {
		return false
	}
	return t.UTC().Format("2006010215") == stamp
}

// MeterRunningMachines debits every RUNNING metered machine one hour of its
// resale price to its OWNING org — the recurring counterpart to the launch debit.
// It is the "a running bound machine debits the org" rule: a machine that stays
// up keeps drawing down the org's credit balance, hour by hour.
//
// Per machine: org is recovered from the machine's own hanzo-org tag (never
// trusted from a client — it is the tag LaunchOrgMachine injected), the hourly
// price comes from the resale catalog (SizeBySlug → PriceToCents), and the debit
// carries the usage id "compute-<machineID>-<YYYYMMDDHH>". Recording is decoupled
// from gating (the machine already ran that hour, so the cost must be recorded);
// enforcement/suspend on a depleted balance is a separate control. A per-machine
// error is logged and does not abort the sweep.
//
// EXACTLY-ONCE PER HOUR: cloud debits one usage id once, so a retried or
// overlapping sweep of the same hour moves no more money; the ticker's per-hour
// single-flight lease (object.ClaimMeterHour) keeps other replicas from sweeping,
// and the LAUNCH hour is skipped here because the launch path already billed it
// under a different id.
//
// No-op when metering is unconfigured or when compute is unconfigured (no platform
// token) — nothing to enumerate, nothing to debit.
func MeterRunningMachines(ctx context.Context) {
	if !ComputeConfigured() || !Billable(ctx, "compute.hourly") {
		return
	}
	ctx, span := telemetry.Span(ctx, "billing.meter.hourly", "", "")
	defer span.End()
	machines, err := ListMeteredMachines()
	if err != nil {
		span.RecordError(err)
		logs.Warning("compute metering: list running machines: %v", err)
		return
	}
	metered, skipped := meterMachines(ctx, machines, time.Now())
	logs.Info("compute metering: hourly sweep done (metered=%d skipped=%d total=%d)", metered, skipped, len(machines))
}

// meterMachines is the pure sweep over a machine set at a fixed wall-clock `now`
// (injected so the idempotency bucket is deterministic under test). It resolves
// each machine's org from its tag, prices it from the resale catalog, and records
// one hour's debit with an hour-bucketed RequestID. Returns (metered, skipped).
// A per-machine failure is logged and skipped — one bad machine never aborts the
// sweep. Isolated from the DO enumeration so the billing contract is testable
// against a fake commerce API.
func meterMachines(ctx context.Context, machines []*Machine, now time.Time) (metered, skipped int) {
	stamp := HourStamp(now)
	for _, m := range machines {
		org := orgFromTag(m.Tag)
		if org == "" {
			skipped++
			logs.Warning("compute metering: machine %s has no %s tag; skipping (unattributable)", m.Id, orgTagKey)
			continue
		}
		// Project is the second attribution dimension recovered from the machine's
		// own tag (empty == the org's default project). It never changes the debit
		// destination (always the org), only the metering Actor, so the ledger is
		// attributable per project.
		project := projectFromTag(m.Tag)
		// Skip the LAUNCH hour: the launch path already debited this machine one
		// hour at create time (usage id = machine id). Metering it again for the
		// same wall-clock hour would double-charge the launch hour (the launch and
		// sweep ids differ, so the ledger sees two acts). A
		// machine with no parseable create time (older rows / test doubles) is
		// metered normally — a launch is never billed unless the launch path ran,
		// so failing to skip only risks the historical status quo, never a miss.
		if CreatedInHour(m.CreatedTime, stamp) {
			continue
		}
		// ONE price resolver, shared with the provision gate. An unresolvable
		// price is a LOUD skip, never a silent $0 debit — a running machine we
		// cannot price is lost revenue, and the log line is how anyone learns.
		cents, err := HourlyCents(m.Size)
		if err != nil {
			skipped++
			logs.Warning("compute metering: machine %s not billed: %v", m.Id, err)
			continue
		}
		if err := RecordCompute(ctx, org, project, cents, m.Size,
			fmt.Sprintf("compute-%s-%s", m.Id, stamp)); err != nil {
			skipped++
			logs.Warning("compute metering: debit machine %s (org %s): %v", m.Id, org, err)
			continue
		}
		metered++
		// Roll a running event into the analytics datastore alongside this
		// hour's debit (best-effort; never blocks or affects the sweep).
		EmitCompute(org, ComputeRunning, m, cents)
	}
	return metered, skipped
}
