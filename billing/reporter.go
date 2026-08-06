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

// reporter.go is the recurring node-pool meter: every ACTIVE pool is debited one
// hour of its resale price, every hour it runs, to the org that owns it.
//
// It debits on the ONE compute meter — service.NewMeteringClient + RecordCompute,
// the same client, the same KMS-synced COMMERCE_SERVICE_TOKEN, the same
// POST /v1/billing/usage, the same X-Org-Id tenant header as every other debit in
// this binary. It used to have its own: its own credential (COMMERCE_TOKEN, a name
// nothing in production sets), its own HTTP client, its own path
// ({base}/api/v1/billing/meter-events — commerce serves no /api/ prefix), and no
// tenant header at all. Three independent reasons the same request could not have
// been billed, so a GPU pool ran free for every hour after its first.
//
// A second meter is how that happens. There is one now.
package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
	"github.com/hanzoai/visor/telemetry"
)

// MeterRunningNodePools debits every ACTIVE node pool one hour of its resale
// price to its owning org — the node-pool half of the hourly money sweep, whose
// machine half is service.MeterRunningMachines.
//
// It is driven by the ticker UNDER the same per-hour single-flight lease as the
// machine sweep (object.ClaimMeterHour), so a pool-hour is claimed once
// cluster-wide. It carries no lease of its own: two leases on the same hour key
// would have one sweep starve the other.
//
// A NO from Billable is loud, never silent: an absent service token stops all
// revenue collection while the pools keep costing us money upstream.
func MeterRunningNodePools(ctx context.Context, now time.Time) {
	if !service.Billable(ctx, "pool.hourly") {
		return
	}
	ctx, span := telemetry.Span(ctx, "billing.meter.pools", "", "")
	defer span.End()

	pools := []*object.NodePool{}
	if err := object.GetAllNodePools(&pools); err != nil {
		span.RecordError(err)
		logs.Warning("pool metering: list node pools: %v", err)
		return
	}
	metered, skipped := meterPools(ctx, pools, now)
	logs.Info("pool metering: hourly sweep done (metered=%d skipped=%d total=%d)", metered, skipped, len(pools))
}

// meterPools is the pure sweep over a pool set at a fixed wall-clock `now`
// (injected so the hour bucket is deterministic under test). Returns
// (metered, skipped). A per-pool failure is logged and skipped — one bad pool
// never aborts the sweep.
func meterPools(ctx context.Context, pools []*object.NodePool, now time.Time) (metered, skipped int) {
	stamp := service.HourStamp(now)
	for _, pool := range pools {
		if pool.State != "Active" || pool.Count < 1 {
			continue
		}
		// Skip the pool's CREATE hour: the provision path already debited it (the
		// ONE launch-hour rule, service.CreatedInHour, shared with the machine
		// sweep), so this hour is billed exactly once. Every LATER hour is billed
		// here — that is the whole job.
		if service.CreatedInHour(pool.CreatedTime, stamp) {
			continue
		}
		org := poolOrg(pool)
		if org == "" {
			skipped++
			logs.Warning("pool metering: pool %s has no owning org; skipping (unattributable)", poolUnit(pool))
			continue
		}
		rate, err := poolHourlyCents(pool)
		if err != nil {
			skipped++
			logs.Warning("pool metering: pool %s not billed: %v", poolUnit(pool), err)
			continue
		}
		cents := rate * int64(pool.Count)
		if err := service.RecordCompute(ctx, org, pool.ProjectID, cents, pool.Size, "running",
			fmt.Sprintf("pool-%s-%s", poolUnit(pool), stamp)); err != nil {
			skipped++
			logs.Warning("pool metering: debit pool %s (org %s, %d cents): %v", poolUnit(pool), org, cents, err)
			continue
		}
		metered++
	}
	return metered, skipped
}

// poolOrg is the org the pool's hours are billed to: the explicit billing OrgID,
// falling back to the pool's IAM owner for rows written before the pool path
// recorded one. Both are server-set — a client body cannot reach either.
func poolOrg(pool *object.NodePool) string {
	if pool.OrgID != "" {
		return pool.OrgID
	}
	return pool.Owner
}

// poolUnit identifies the pool in the idempotency key and the logs: its upstream
// pool id, or its owner/name id when it has no upstream linkage yet.
func poolUnit(pool *object.NodePool) string {
	if pool.PoolID != "" {
		return pool.PoolID
	}
	return pool.GetId()
}

// poolHourlyCents resolves the pool's per-node hourly rate, and REFUSES rather
// than returning a zero. The pool's own persisted CostPerHour is authoritative —
// it was stamped from the catalog at provision time and is the only price a slug
// the upstream has since delisted still has. Only a pool with no stored rate
// falls back to the live resale catalog (service.HourlyCents, the ONE price
// resolver), and an unresolvable rate there is an ERROR: billing an H100 pool at
// zero is the same leak as not billing it at all.
func poolHourlyCents(pool *object.NodePool) (int64, error) {
	if pool.CostPerHour > 0 {
		return pool.CostPerHour, nil
	}
	return service.HourlyCents(pool.Size)
}
