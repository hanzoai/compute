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

// reporter.go is the recurring node-pool meter: every pool that is RUNNING is
// debited one hour of its resale price, every hour it runs, to the org that owns
// it.
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
//
// WHAT IT BILLS FROM is the other half, and it changed for the same class of
// reason. The sweep used to read the DB row and nothing else — but the row's key
// is (Owner, Name), and the Name comes from the customer's own create body. A row
// a customer can name is a row a customer can collide with, delete, or leave
// stale, and each of those turns into free compute. The provider knows what it is
// actually running; the row does not. So for the configured cloud account the PROVIDER is
// the authority and the row is a cache of it. See billableUnits.
package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/compute/logs"
	"github.com/hanzoai/compute/object"
	"github.com/hanzoai/compute/service"
	"github.com/hanzoai/compute/telemetry"
)

// MeterRunningNodePools debits every running node pool one hour of its resale
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

	rows := []*object.NodePool{}
	if err := object.GetAllNodePools(&rows); err != nil {
		span.RecordError(err)
		logs.Warning("pool metering: list node pools: %v", err)
		return
	}

	// The provider is the AUTHORITY for the configured cloud account, so a sweep that cannot
	// reach it does not bill. Without the live pools there is no way to tell a
	// running pool from a deleted one, or a pool that autoscaled from one that did
	// not — and the rows alone are exactly the answer that was wrong. A missed
	// hour is reconcilable; an hour billed against stale rows is a wrong invoice.
	//
	// An UNCONFIGURED configured cloud account is not a failure: it means there are no platform
	// clusters at all, so the live set is legitimately empty and every row is a
	// tenant's own (BYOC) pool.
	var live []service.LivePool
	if service.ComputeConfigured() {
		var err error
		if live, err = service.ListLivePools(ctx); err != nil {
			span.RecordError(err)
			logs.Warning("pool metering: cannot reach the provider (%v) — NO node pools billed this hour; "+
				"billing from stale rows would invoice pools that may no longer exist", err)
			return
		}
	}

	metered, skipped := meterPools(ctx, live, rows, now)
	logs.Info("pool metering: hourly sweep done (metered=%d skipped=%d live=%d rows=%d)",
		metered, skipped, len(live), len(rows))
}

// unit is ONE pool-hour to debit: who pays, for what, how many nodes, at what
// stamped rate, and the hour the provision path already billed. It is what both
// sources — the provider's live pools and the store's rows — resolve to, so the
// debit below is written once and knows nothing about where a pool came from.
type unit struct {
	org     string
	project string
	// id names the pool in the idempotency key and the logs: the upstream pool id
	// wherever one exists, because that is the one name for a pool no customer can
	// change.
	id    string
	size  string
	nodes int
	// centsHour is the rate STAMPED at provision time, or 0 when none applies and
	// the live catalog must answer.
	centsHour int64
	// created is the RFC3339 hour the provision path debited, which this sweep
	// therefore skips.
	created string
}

// meterPools is the pure sweep at a fixed wall-clock `now` (injected so the hour
// bucket is deterministic under test): resolve both sources into billable units,
// then debit each one. Returns (metered, skipped). A per-pool failure is logged
// and skipped — one bad pool never aborts the sweep.
func meterPools(ctx context.Context, live []service.LivePool, rows []*object.NodePool, now time.Time) (metered, skipped int) {
	stamp := service.HourStamp(now)
	for _, u := range billableUnits(live, rows) {
		if u.nodes < 1 {
			continue // no nodes, no cost
		}
		// Skip the pool's CREATE hour: the provision path already debited it (the
		// ONE launch-hour rule, service.CreatedInHour, shared with the machine
		// sweep), so this hour is billed exactly once. Every LATER hour is billed
		// here — that is the whole job.
		if service.CreatedInHour(u.created, stamp) {
			continue
		}
		if u.org == "" {
			skipped++
			logs.Warning("pool metering: pool %s has no owning org; skipping (unattributable)", u.id)
			continue
		}
		rate, err := unitRate(u)
		if err != nil {
			skipped++
			logs.Warning("pool metering: pool %s not billed: %v", u.id, err)
			continue
		}
		cents := rate * int64(u.nodes)
		if err := service.RecordCompute(ctx, u.org, u.project, cents, u.size, "running",
			fmt.Sprintf("pool-%s-%s", u.id, stamp)); err != nil {
			skipped++
			logs.Warning("pool metering: debit pool %s (org %s, %d cents): %v", u.id, u.org, cents, err)
			continue
		}
		metered++
	}
	return metered, skipped
}

// billableUnits is the ONE answer to "what does this hour bill", and the whole
// point is WHERE each field comes from.
//
// The PROVIDER is authoritative for the configured cloud account: which pools exist,
// which cluster and org they belong to, and how many nodes they are ACTUALLY
// running. The stored row is a CACHE of that. It contributes the two things the
// provider does not know — the rate the org was authorized at and the project the
// spend belongs to — plus the create hour the provision path already debited. It
// contributes NOTHING to whether a pool bills, or for how many nodes.
//
// That reassignment closes three leaks at once, and none of them could be closed
// on the row's side, because the row's key is (Owner, Name) and the customer
// writes the Name:
//
//   - two clusters whose seed pools take the same name collided on that key, and
//     the second create OVERWROTE the first one's row. One cluster then ran free
//     for the rest of its life. Now both are live pools, so both bill.
//   - a delete naming a row and no upstream pool dropped the meter of record
//     while the cluster kept running. Now dropping the row changes nothing: the
//     pool is still on the provider, so it still bills.
//   - an autoscaled pool grew from one node to sixteen and kept billing one,
//     because nothing writes the new count anywhere compute controls. Now the count
//     comes from the provider, so it bills sixteen from the next hour.
//
// A row whose cluster the configured cloud account does NOT have is billed FROM THE ROW.
// That is a BYOC pool — provisioned on a tenant's own provider credentials and
// invisible to the provider token — and the row is the only record of it there is.
// Splitting on the CLUSTER rather than on anything written in the row is what
// makes double-billing impossible: every pool falls on exactly one side of that
// line, and the provider owns everything on its side.
func billableUnits(live []service.LivePool, rows []*object.NodePool) []unit {
	platform := map[string]bool{}
	for _, p := range live {
		platform[p.ClusterID] = true
	}
	cache := indexRows(rows)

	units := make([]unit, 0, len(live)+len(rows))
	for _, p := range live {
		u := unit{
			org: p.Org, id: unitID(p.PoolID, p.ClusterID+"/"+p.Name),
			size: p.Size, nodes: p.Nodes, created: p.Created,
		}
		if row := cache.find(p); row != nil {
			u.project = row.ProjectID
			if u.org == "" {
				// An untagged cluster is still attributable if the provision path
				// wrote down who asked for it.
				u.org = rowOrg(row)
			}
			if u.created == "" {
				u.created = row.CreatedTime
			}
			// The stamped rate belongs to the size it was stamped FOR. If the
			// provider reports a different slug than the row remembers, that price
			// is not this pool's price — let the catalog answer instead of billing
			// a GPU pool at the rate of the CPU pool that used to carry its name.
			if row.Size == p.Size {
				u.centsHour = row.CostPerHour
			}
		}
		units = append(units, u)
	}

	for _, r := range rows {
		if platform[r.ClusterID] {
			continue // the provider already spoke for this cluster
		}
		if r.State != "Active" || r.Count < 1 {
			continue
		}
		units = append(units, unit{
			org: rowOrg(r), project: r.ProjectID, id: unitID(r.PoolID, r.GetId()),
			size: r.Size, nodes: r.Count, centsHour: r.CostPerHour, created: r.CreatedTime,
		})
	}
	return units
}

// poolCache indexes the stored rows by the two ways a live pool can find its own:
// its upstream (cluster, pool) id pair, and — for a cluster's SEED pool, whose row
// is written before the upstream pool id is known — its (cluster, name).
//
// It is a lookup for RATE and PROJECT only. A wrong hit cannot redirect a bill:
// the org comes from the provider's own cluster tag and the row is consulted for
// it only when the cluster carries none.
type poolCache struct {
	byPool map[string]*object.NodePool
	byName map[string]*object.NodePool
}

func indexRows(rows []*object.NodePool) poolCache {
	c := poolCache{byPool: map[string]*object.NodePool{}, byName: map[string]*object.NodePool{}}
	for _, r := range rows {
		if r.ClusterID == "" {
			continue
		}
		if r.PoolID != "" {
			c.byPool[r.ClusterID+"/"+r.PoolID] = r
		}
		if r.Name != "" {
			c.byName[r.ClusterID+"/"+r.Name] = r
		}
	}
	return c
}

func (c poolCache) find(p service.LivePool) *object.NodePool {
	if r := c.byPool[p.ClusterID+"/"+p.PoolID]; r != nil {
		return r
	}
	return c.byName[p.ClusterID+"/"+p.Name]
}

// unitID prefers the upstream pool id and falls back to a local identity for a
// pool that has none yet.
func unitID(poolID, fallback string) string {
	if poolID != "" {
		return poolID
	}
	return fallback
}

// rowOrg is the org a row's hours are billed to: the explicit billing OrgID,
// falling back to the row's IAM owner for rows written before the pool path
// recorded one. Both are server-set — a client body cannot reach either.
func rowOrg(pool *object.NodePool) string {
	if pool.OrgID != "" {
		return pool.OrgID
	}
	return pool.Owner
}

// unitRate resolves a unit's per-node hourly rate, and REFUSES rather than
// returning a zero. The rate stamped at provision time is authoritative — it is
// the only price a slug the upstream has since delisted still has. A unit with no
// stamped rate falls back to the live resale catalog (service.HourlyCents, the ONE
// price resolver), and an unresolvable rate there is an ERROR: billing an H100
// pool at zero is the same leak as not billing it at all.
func unitRate(u unit) (int64, error) {
	if u.centsHour > 0 {
		return u.centsHour, nil
	}
	return service.HourlyCents(u.size)
}
