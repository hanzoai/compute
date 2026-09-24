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

// fleet.go is the fleet-billing orchestrator — the ONE place the three connected-
// compute tiers are metered, all through the one commerce client (service.Record),
// per org+project, so a customer sees ONE invoice
// however their compute is connected:
//
//	(b) BYO hardware/device → flat $1/mo per connected GPU/device (monthly).
//	(c) hanzo.network validator → FREE (verified on-chain; never billed).
//
// Tier (a), 1% of a BYOC cloud account's spend, read the account's cost API with
// the key the provider row stored. A row stores no key now, so that tier is not
// collected.
//
// The tier is a property of the compute source (a Provider vs a FleetWorker.Kind),
// resolved here; there is one debit path, three rates. Money safety: every unit is
// claimed cluster-wide via object BillingLease before it is billed, and debited
// under the unit's own id, which the ledger debits once.
// Honesty: no spend or exemption is ever fabricated — an unreadable cost is skipped
// (no fee) and an unverified validator is billed (flagged), never the reverse.
//
// Brand policy: the upstream cloud TYPE (AWS/DO/…) never leaves the cost readers; the
// metering line's Model carries only the tier and the customer's OWN provider/worker
// label, never an upstream provider name.
package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/compute/chain"
	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/telemetry"
)

// tierDevice is the metering line's tier for a connected device.
const tierDevice = "device"

// deviceMonthlyCents is the flat tier-(b) rate: $1.00 per connected device per month.
const deviceMonthlyCents = 100

// meterFleetLine is the ONE fleet debit: it records cents to the org's commerce
// ledger, attributed to org+project, and mirrors it as an OTel metric. Service is
// the brand-neutral "fleet"; Model is "<tier>:<label>" where label is the
// customer's OWN provider/worker name (never an upstream cloud name). unit names
// the billing unit the caller already claimed, and is the debit's id.
func meterFleetLine(ctx context.Context, org, project, tier, label string, cents int64, unit string) {
	if cents <= 0 {
		return
	}
	if err := service.Record(ctx, service.Charge{
		ID: unit, Org: org, Project: project, Cents: cents,
		Service: "fleet", Model: tier + ":" + label,
	}); err != nil {
		logs.Warning("fleet billing: meter %s line for org %s (%d cents): %v", tier, org, cents, err)
		return
	}
	telemetry.CountMetered(ctx, org, project, tier, cents)
	logs.Info("fleet billing: metered %s %d cents to %s (project=%q, unit=%s)", tier, cents, org, project, unit)
}

// meterWorkerDevice bills ONE connected worker for the current month: the flat
// $1/device tier-(b) rate — UNLESS the worker is a verified hanzo.network validator
// (tier c), which is free. The validator exemption is granted ONLY on a positive,
// verified on-chain read; an unwired lookup or a lookup error leaves the worker
// billed as a device, flagged in the log (never fabricate an on-chain exemption).
// Single-flight per (worker, month) via BillingLease, so connect-time and the monthly
// sweep can both call this without double-billing.
func meterWorkerDevice(ctx context.Context, w *object.FleetWorker, now time.Time) {
	if w.Kind == object.FleetWorkerValidator {
		exempt, status, err := chain.ValidatorExemption(ctx, w.ValidatorAddress)
		if err != nil {
			logs.Warning("fleet billing: validator lookup for %s/%s: %v", w.Owner, w.Name, err)
		}
		if exempt {
			logs.Info("fleet billing: worker %s/%s is a verified validator — free (native)", w.Owner, w.Name)
			return
		}
		logs.Info("fleet billing: worker %s/%s validator status=%q — billed as device", w.Owner, w.Name, status)
	}

	devices := max(w.DeviceCount,
		// a connected box is at least one device
		1)
	cents := int64(devices) * deviceMonthlyCents

	month := now.UTC().Format("200601")
	unit := fmt.Sprintf("device:%s:%s:%s", w.Owner, w.Name, month)
	if !object.ClaimBillingUnit(unit, now) {
		return // already billed this worker this month (or another replica did)
	}
	meterFleetLine(ctx, w.Owner, w.Project, tierDevice, w.Name, cents, unit)
}

// MeterConnectedDevices is the monthly tier-(b)/(c) sweep: bill every CONNECTED BYO
// worker $1/device for the current month (validators exempt). It is safe to run more
// than once a month — the per-(worker, month) lease makes each worker's fee
// exactly-once.
func MeterConnectedDevices(ctx context.Context, now time.Time) {
	if !service.Billable(ctx, "device.monthly") {
		return
	}
	ctx, span := telemetry.Span(ctx, "billing.meter.devices", "", "")
	defer span.End()

	workers, err := object.GetConnectedFleetWorkers()
	if err != nil {
		span.RecordError(err)
		logs.Warning("fleet billing: list connected workers: %v", err)
		return
	}
	for _, w := range workers {
		meterWorkerDevice(ctx, w, now)
	}
	logs.Info("fleet billing: device meter sweep done (connected=%d)", len(workers))
}

// BillWorkerOnConnect arms billing for a freshly-connected worker — the "armed on
// connect" half of tier (b). It bills the current month immediately (idempotent per
// month via the same lease the monthly sweep uses), so a box connected mid-month is
// billed at once, and the monthly sweep never double-bills it.
func BillWorkerOnConnect(ctx context.Context, w *object.FleetWorker) {
	if !service.Billable(ctx, "device.connect") {
		return
	}
	meterWorkerDevice(ctx, w, time.Now())
}
