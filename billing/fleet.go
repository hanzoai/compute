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
// compute tiers are metered, all through the canonical commerce/metering path
// (service.NewMeteringClient), per org+project, so a customer sees ONE invoice
// however their compute is connected:
//
//	(a) BYOC cloud account  → 1% of the account's cloud spend (daily, incremental).
//	(b) BYO hardware/device → flat $1/mo per connected GPU/device (monthly).
//	(c) hanzo.network validator → FREE (verified on-chain; never billed).
//
// The tier is a property of the compute source (a Provider vs a FleetWorker.Kind),
// resolved here; there is one debit path, three rates. Money safety: every unit is
// claimed cluster-wide via object BillingLease before it is billed, because visor
// runs replicas: 2 with no leader election and commerce does not dedup on requestId.
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

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/visor/chain"
	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/telemetry"
)

// Fleet-billing tiers (the metering line's Status + telemetry tier).
const (
	tierBYOC   = "byoc"
	tierDevice = "device"
)

// deviceMonthlyCents is the flat tier-(b) rate: $1.00 per connected device per month.
const deviceMonthlyCents = 100

// onePercentFeeCents is the tier-(a) rate: 1% of a cloud-spend increment, rounded to
// the nearest cent (half up). Zero/negative spend yields no fee.
func onePercentFeeCents(spendCents int64) int64 {
	if spendCents <= 0 {
		return 0
	}
	return (spendCents + 50) / 100
}

// meterFleetLine is the ONE fleet debit: it records cents to the org's commerce
// ledger, attributed to org+project via the shared MeterActor, and mirrors it as an
// OTel metric. Provider is the brand-neutral "fleet"; Model is "<tier>:<label>" where
// label is the customer's OWN provider/worker name (never an upstream cloud name).
// requestID is the billing unit (idempotency hint; the real once-per-unit guarantee
// is the BillingLease the caller already claimed).
func meterFleetLine(ctx context.Context, org, project, tier, label string, cents int64, requestID string) {
	if cents <= 0 {
		return
	}
	client := service.NewMeteringClient(org)
	if _, err := client.Record(ctx, metering.Usage{
		User:        org,
		Actor:       service.MeterActor(org, project),
		Org:         org,
		Currency:    "usd",
		AmountCents: cents,
		Provider:    "fleet",
		Model:       tier + ":" + label,
		Status:      tier,
		RequestID:   requestID,
	}); err != nil {
		logs.Warning("fleet billing: meter %s line for org %s (%d cents): %v", tier, org, cents, err)
		return
	}
	telemetry.CountMetered(ctx, org, project, tier, cents)
	logs.Info("fleet billing: metered %s %d cents to %s (project=%q, unit=%s)", tier, cents, org, project, requestID)
}

// CollectBYOCCosts is the daily tier-(a) collector. For every org's active BYOC cloud
// provider it reads month-to-date spend via that cloud's cost API and meters 1% of the
// increment since the last run to org+project. No spend → no fee; an unreadable cost
// (unconfigured scope, transient error) is skipped for this run and recovered on the
// next (the cursor is only advanced on a successful read). Single-flight per
// (provider, day) via BillingLease, so replicas never double-bill.
func CollectBYOCCosts(ctx context.Context, now time.Time) {
	if !service.MeteringConfigured() {
		return
	}
	ctx, span := telemetry.Span(ctx, "billing.collect.byoc", "", "")
	defer span.End()

	providers, err := object.GetAllActiveCloudProviders()
	if err != nil {
		span.RecordError(err)
		logs.Warning("fleet billing: list BYOC providers: %v", err)
		return
	}
	day := now.UTC().Format("20060102")
	month := now.UTC().Format("200601")

	billed := 0
	for _, p := range providers {
		reader, err := service.NewCostReader(p.Type, p.ClientId, p.ClientSecret, p.Region, p.CostReadScope)
		if err != nil {
			continue // ErrCostUnavailable etc. — cost-read not wired for this account; no fee
		}
		mtd, err := reader.MonthToDateCents(ctx, now)
		if err != nil {
			logs.Warning("fleet billing: read spend for %s/%s: %v", p.Owner, p.Name, err)
			continue // transient/unavailable → skip this run, recover next (cursor untouched)
		}
		if mtd <= 0 {
			continue // no spend → no fee
		}
		// Claim the day for this provider AFTER a successful read, so only the lease
		// winner advances the cursor and bills; losers skip.
		unit := fmt.Sprintf("byoc:%s:%s:%s", p.Owner, p.Name, day)
		if !object.ClaimBillingUnit(unit, now) {
			continue
		}
		delta, err := object.AdvanceCostCursor(p.Owner, p.Name, month, mtd)
		if err != nil {
			logs.Warning("fleet billing: advance cost cursor %s/%s: %v", p.Owner, p.Name, err)
			continue
		}
		fee := onePercentFeeCents(delta)
		if fee <= 0 {
			continue
		}
		meterFleetLine(ctx, p.Owner, p.Project, tierBYOC, p.Name, fee, unit)
		billed++
	}
	logs.Info("fleet billing: BYOC cost sweep done (providers=%d billed=%d)", len(providers), billed)
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

	devices := w.DeviceCount
	if devices < 1 {
		devices = 1 // a connected box is at least one device
	}
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
	if !service.MeteringConfigured() {
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
	if !service.MeteringConfigured() {
		return
	}
	meterWorkerDevice(ctx, w, time.Now())
}
