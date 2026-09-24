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

// metering.go is the ONE commerce metering path for hosted compute. The launch
// debit (controllers/compute.go), the start debit (SetOrgMachineState) and the
// recurring hourly debit (MeterRunningMachines, driven by task/ticker) all price
// from the catalog (HourlyCents) and debit through RecordCompute on cloud's
// commerce API (commerce.go) — there is no second metering path for /v1
// machines. Fleet billing (billing/) debits through the same client.
package service

import (
	"context"
	"time"

	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/telemetry"
)

// meteringProvider labels hosted-compute usage in the commerce ledger so spend
// is attributable to this surface (product), independent of the size recorded as
// the Model.
const meteringProvider = "compute"

// HourStamp is the per-hour bucket in the usage id: the same running resource
// metered twice within one wall-clock hour carries the SAME id, and cloud debits
// one id once. The single-flight lease in the ticker (object.ClaimMeterHour) keeps
// a second replica from sweeping at all; the id is what makes a retried debit
// harmless.
func HourStamp(now time.Time) string { return now.UTC().Format("2006010215") }

// MeterID names one running hour of a machine: the usage id the hourly sweep
// debits, and the id a start debits its first hour under, so the two can never
// charge the same hour twice.
func MeterID(machineID string, now time.Time) string {
	return "compute-" + machineID + "-" + HourStamp(now)
}

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

// mostCatchUp bounds how many hours one sweep bills a machine. Hours are owed
// from the last one billed, so this is reached only by an outage longer than a
// week or a ledger that lost its rows, and in both cases billing a week at a
// time is safer than billing a machine's whole life in one sweep.
const mostCatchUp = 7 * 24

// MeterRunningMachines debits every RUNNING metered machine every hour of its
// price it has run and not yet been billed for, through the hour now falls in,
// to its OWNING org — the recurring counterpart to the launch debit. A machine
// that stays up keeps drawing down the org's credit balance, hour by hour, and
// an hour a sweep missed is billed by the next one.
//
// Per machine: org is recovered from the machine's own hanzo-org tag (never
// trusted from a client — it is the tag LaunchOrgMachine set), the hourly price
// comes from the catalog (HourlyCents), and each hour's debit carries the usage
// id MeterID: "compute-<machineID>-<YYYYMMDDHH>". Recording is decoupled from
// gating (the machine already ran that hour, so the cost must be recorded);
// enforcement/suspend on a depleted balance is a separate control. A per-machine
// error is logged and does not abort the sweep.
//
// EXACTLY-ONCE PER HOUR is the Ledger: the sweep advances each machine's mark
// before it debits and debits only the hours the mark moved over, the launch and
// a start advance the same mark, and the ticker's per-hour single-flight lease
// (object.ClaimMeterHour) keeps other replicas from sweeping at all.
//
// No-op when metering is unconfigured or when the hosted account is unconfigured
// — nothing to enumerate, nothing to debit.
func MeterRunningMachines(ctx context.Context, now time.Time) {
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
	metered, skipped := meterMachines(ctx, machines, now)
	logs.Info("compute metering: hourly sweep done (metered=%d skipped=%d total=%d)", metered, skipped, len(machines))
}

// owed is one machine's unbilled hours and what each costs.
type owed struct {
	m       *Machine
	org     string
	project string
	cents   int64
	hours   []time.Time
}

// meterMachines is the pure sweep over a machine set at a fixed wall-clock `now`
// (injected so the idempotency bucket is deterministic under test). It resolves
// each machine's org from its tag, prices it from the catalog, finds the hours it
// owes, advances every mark at once, and debits each owed hour under its own
// MeterID. Returns (metered hours, skipped). A per-machine failure is logged and
// skipped — one bad machine never aborts the sweep.
func meterMachines(ctx context.Context, machines []*Machine, now time.Time) (metered, skipped int) {
	current, _ := parseHour(hourOf(now))
	var due []owed
	marks := map[string]string{}
	for _, m := range machines {
		org := orgFromTag(m.Tag)
		if org == "" {
			skipped++
			logs.Warning("compute metering: machine %s has no %s tag; skipping (unattributable)", m.Id, orgTagKey)
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
		hours, err := unbilled(m, current)
		if err != nil {
			skipped++
			logs.Warning("compute metering: machine %s: read billed hours: %v", m.Id, err)
			continue
		}
		if len(hours) == 0 {
			continue
		}
		// Project is the second attribution dimension recovered from the
		// machine's own tag (empty == the org's default project). It never changes
		// the debit destination (always the org), only the metering Actor.
		due = append(due, owed{m: m, org: org, project: projectFromTag(m.Tag), cents: cents, hours: hours})
		marks[m.Id] = hourOf(current)
	}
	if len(marks) == 0 {
		return metered, skipped
	}
	// The marks move BEFORE any money does, so a debit that fails under-bills
	// its hour rather than a retried sweep charging it twice. A write that fails
	// part-way bills the machines it moved and leaves the rest owed.
	moved, err := billed().Advance(marks)
	if err != nil {
		logs.Warning("compute metering: record billed hours: %v — machines not recorded stay owed", err)
	}
	for _, d := range due {
		if !moved[d.m.Id] {
			continue
		}
		for _, h := range d.hours {
			if err := RecordCompute(ctx, d.org, d.project, d.cents, d.m.Size, MeterID(d.m.Id, h)); err != nil {
				skipped++
				logs.Warning("compute metering: debit machine %s hour %s (org %s): %v", d.m.Id, hourOf(h), d.org, err)
				continue
			}
			metered++
			// Roll a running event into the analytics datastore alongside this
			// hour's debit (best-effort; never blocks or affects the sweep).
			EmitCompute(d.org, ComputeRunning, d.m, d.cents)
		}
	}
	return metered, skipped
}

// unbilled is every whole clock hour machine m has run and not been billed for,
// through current.
//
// m.CreatedTime is EC2's LaunchTime, the machine's LAST start, and the machine
// has run without a stop since, so no hour before it is owed. A machine with a
// mark owes every hour after the mark from its last start on. A machine with no
// mark was billed before the ledger held it (its launch or start hour under its
// own id), so it owes the current hour only, and not even that in the hour it
// started.
func unbilled(m *Machine, current time.Time) ([]time.Time, error) {
	mark, err := billed().Through(m.Id)
	if err != nil {
		return nil, err
	}
	started, known := time.Time{}, false
	if t, err := time.Parse(time.RFC3339, m.CreatedTime); err == nil {
		started, known = t.UTC().Truncate(time.Hour), true
	}
	var from time.Time
	if last, ok := parseHour(mark); ok {
		from = last.Add(time.Hour)
		if known && started.After(from) {
			from = started
		}
	} else {
		if known && !started.Before(current) {
			return nil, nil
		}
		from = current
	}
	if earliest := current.Add(-mostCatchUp * time.Hour); from.Before(earliest) {
		logs.Warning("compute metering: machine %s owes hours from %s; billing the last %d", m.Id, hourOf(from), mostCatchUp)
		from = earliest
	}
	var hours []time.Time
	for h := from; !h.After(current); h = h.Add(time.Hour) {
		hours = append(hours, h)
	}
	return hours, nil
}

// LaunchCharge names a launch's debit: its first hour, under the id the sweep
// and a start use for that hour, recorded in the ledger so neither charges it
// again. The launch hour is owed whatever the ledger says, so a ledger that
// cannot be written still debits it, and the sweep then owes nothing for the
// hour the machine started in.
func LaunchCharge(machine string, now time.Time) string {
	if _, err := billed().Advance(map[string]string{machine: hourOf(now)}); err != nil {
		logs.Warning("compute metering: record launch hour of %s: %v", machine, err)
	}
	return MeterID(machine, now)
}

// startCharge names a start's debit, or "" when its hour is already billed: a
// machine launched, stopped and started in one clock hour pays for that hour
// once. A ledger that cannot be written charges nothing here, and the sweep
// bills the hour from the machine's start.
func startCharge(machine string, now time.Time) string {
	moved, err := billed().Advance(map[string]string{machine: hourOf(now)})
	if err != nil {
		logs.Warning("compute metering: record start hour of %s: %v", machine, err)
		return ""
	}
	if !moved[machine] {
		return ""
	}
	return MeterID(machine, now)
}
