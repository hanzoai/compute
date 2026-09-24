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
	"fmt"
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
// EXACTLY-ONCE PER HOUR is the Ledger and the meter id together. The sweep
// debits each owed hour under its own MeterID and moves the machine's mark only
// over the hours whose debit landed, the launch and a start record their hour
// the same way, and the ticker's per-hour single-flight lease
// (object.ClaimMeterHour) keeps other replicas from sweeping at all. A debit
// that fails leaves its hour owed for the next sweep; a mark that fails to write
// after its debit landed re-sends that hour's id, which commerce debits once.
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

// owed is one machine's unbilled hours and what each costs, under one ledger
// key: its running hours under its id, its stopped hours under its disk key.
type owed struct {
	m       *Machine
	org     string
	project string
	key     string
	cents   int64
	model   string
	id      func(time.Time) string
	hours   []time.Time
}

// diskKey is the ledger key a machine's stopped hours are marked under, apart
// from its running hours, so a stopped hour and a running hour are each billed
// at their own price and neither mark hides the other.
func diskKey(machine string) string { return machine + "/disk" }

// DiskMeterID names one stopped hour of a machine: its disk.
func DiskMeterID(machineID string, now time.Time) string {
	return "compute-" + machineID + "-disk-" + HourStamp(now)
}

// meterMachines is the pure sweep over a machine set at a fixed wall-clock `now`
// (injected so the idempotency bucket is deterministic under test). It resolves
// each machine's org from its tag, prices it from the catalog, finds the hours it
// owes, and debits them in order under their own meter ids; each ledger key's
// mark moves over the hours whose debit landed, and the marks are written once,
// after every debit. Returns (metered hours, skipped). A per-machine failure is
// logged and skipped — one bad machine never aborts the sweep.
func meterMachines(ctx context.Context, machines []*Machine, now time.Time) (metered, skipped int) {
	current, _ := parseHour(hourOf(now))
	marks := map[string]string{}
	for _, m := range machines {
		d, err := owe(m, current)
		if err != nil {
			skipped++
			logs.Warning("compute metering: machine %s not billed: %v", m.Id, err)
			continue
		}
		for _, h := range d.hours {
			if err := RecordCompute(ctx, d.org, d.project, d.cents, d.model, d.id(h)); err != nil {
				// The hours from here on stay owed, in order, for the next sweep.
				skipped++
				logs.Warning("compute metering: debit %s hour %s (org %s): %v", d.key, hourOf(h), d.org, err)
				break
			}
			metered++
			marks[d.key] = hourOf(h)
			// Roll a running event into the analytics datastore alongside a
			// running hour's debit (best-effort; never blocks or affects the sweep).
			if d.key == d.m.Id {
				EmitCompute(d.org, ComputeRunning, d.m, d.cents)
			}
		}
	}
	if len(marks) > 0 {
		if _, err := billed().Advance(marks); err != nil {
			logs.Warning("compute metering: record billed hours: %v — the next sweep re-sends them under the same ids", err)
		}
	}
	return metered, skipped
}

// owe is what machine m owes through current: its unbilled hours, what each
// costs, and the ledger key and meter id they are billed under. An error is a
// machine that cannot be billed at all — no org, no price, no ledger.
func owe(m *Machine, current time.Time) (owed, error) {
	org := orgFromTag(m.Tag)
	if org == "" {
		return owed{}, fmt.Errorf("no %s tag (unattributable)", orgTagKey)
	}
	// ONE price resolver, shared with the provision gate. An unresolvable price
	// is a LOUD skip, never a silent $0 debit — a machine we cannot price is
	// lost revenue, and the log line is how anyone learns.
	d := owed{m: m, org: org, project: projectFromTag(m.Tag), key: m.Id, model: m.Size,
		id: func(h time.Time) string { return MeterID(m.Id, h) }}
	var err error
	if m.State == "Stopped" {
		d.key, d.model = diskKey(m.Id), m.Size+"/stopped"
		d.id = func(h time.Time) string { return DiskMeterID(m.Id, h) }
		if d.cents, err = StoppedCents(m.Size); err == nil {
			d.hours, err = stoppedHours(m, current)
		}
	} else if d.cents, err = HourlyCents(m.Size); err == nil {
		d.hours, err = unbilled(m, current)
	}
	return d, err
}

// stoppedHours is every whole clock hour a stopped machine owes for its disk,
// through current: the hours after the last one billed either way, running or
// stopped — a machine's running price already carries its disk. A machine with
// neither mark owes the current hour only.
func stoppedHours(m *Machine, current time.Time) ([]time.Time, error) {
	run, err := billed().Through(m.Id)
	if err != nil {
		return nil, err
	}
	disk, err := billed().Through(diskKey(m.Id))
	if err != nil {
		return nil, err
	}
	from := current
	if last, ok := parseHour(max(run, disk)); ok {
		from = last.Add(time.Hour)
	}
	if earliest := current.Add(-mostCatchUp * time.Hour); from.Before(earliest) {
		logs.Warning("compute metering: stopped machine %s owes hours from %s; billing the last %d", m.Id, hourOf(from), mostCatchUp)
		from = earliest
	}
	var hours []time.Time
	for h := from; !h.After(current); h = h.Add(time.Hour) {
		hours = append(hours, h)
	}
	return hours, nil
}

// unbilled is every whole clock hour machine m has run and not been billed for,
// through current.
//
// m.CreatedTime is EC2's LaunchTime, the machine's LAST start, and the machine
// has run without a stop since, so no hour before it is owed. A machine with a
// mark owes every hour after the mark from its last start on. A machine with no
// mark owes every hour from its last start: a launch or start whose debit failed
// recorded nothing, so the sweep is what bills that hour. One whose start is
// unknown owes the current hour.
func unbilled(m *Machine, current time.Time) ([]time.Time, error) {
	mark, err := billed().Through(m.Id)
	if err != nil {
		return nil, err
	}
	started, known := time.Time{}, false
	if t, err := time.Parse(time.RFC3339, m.CreatedTime); err == nil {
		started, known = t.UTC().Truncate(time.Hour), true
	}
	from := current
	if last, ok := parseHour(mark); ok {
		from = last.Add(time.Hour)
		if known && started.After(from) {
			from = started
		}
	} else if known {
		from = started
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
// and a start use for that hour. It records nothing; MarkBilled does, once the
// debit has landed.
func LaunchCharge(machine string, now time.Time) string {
	return MeterID(machine, now)
}

// startCharge names a start's debit, or "" when its hour is already billed: a
// machine launched, stopped and started in one clock hour pays for that hour
// once. A ledger that cannot be read charges nothing here, and the sweep bills
// the hour from the machine's start.
func startCharge(machine string, now time.Time) string {
	mark, err := billed().Through(machine)
	if err != nil {
		logs.Warning("compute metering: read billed hours of %s: %v", machine, err)
		return ""
	}
	if mark >= hourOf(now) {
		return ""
	}
	return MeterID(machine, now)
}

// MarkBilled records machine billed through the hour at falls in. It is called
// only once that hour's debit has landed, so an hour the ledger holds is an hour
// commerce charged. A mark that fails to write leaves the hour looking owed, and
// the next sweep re-sends its id, which commerce debits once.
func MarkBilled(machine string, at time.Time) {
	if _, err := billed().Advance(map[string]string{machine: hourOf(at)}); err != nil {
		logs.Warning("compute metering: record billed hour %s of %s: %v", hourOf(at), machine, err)
	}
}
