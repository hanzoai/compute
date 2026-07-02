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
// (MeterRunningMachines, driven by task/ticker) build their client with
// NewMeteringClient and price with PriceToCents — there is no second metering
// path for /v1 machines. (The legacy billing/reporter.go meters DOKS NODE POOLS
// on a different, node-pool-specific event API; it is orthogonal and untouched.)
package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/beego/beego/logs"
	"github.com/hanzoai/commerce/metering"
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

// NewMeteringClient builds the commerce metering client for an org. The commerce
// base URL and the admin-scoped service token both come from the environment (the
// operator wires the token from KMS as COMMERCE_SERVICE_TOKEN). When the token is
// absent the client fails closed on Authorize, so real launches are denied while
// quotes still work, and the recurring meter is a no-op (Record short-circuits on
// !Enabled()). This is the SAME client construction the launch path uses, so both
// key the same per-org ledger.
func NewMeteringClient(org string) *metering.Client {
	base := strings.TrimSpace(os.Getenv("COMMERCE_URL"))
	if base == "" {
		base = "http://commerce.hanzo.svc.cluster.local:8001"
	}
	client, _ := metering.New(metering.Config{
		BaseURL:   base,
		Token:     strings.TrimSpace(os.Getenv("COMMERCE_SERVICE_TOKEN")),
		Org:       org,
		TierAware: true,
		Timeout:   5 * time.Second,
	})
	return client
}

// MeteringConfigured reports whether the recurring meter will actually debit.
// The client's own Enabled() only checks the base URL (which always defaults to
// the in-cluster commerce), so the real "is billing wired" signal is the
// operator-provisioned service token (KMS-synced COMMERCE_SERVICE_TOKEN) — the
// same credential the launch path needs to authorize. Absent ⇒ the sweep is a
// safe no-op: an unconfigured deployment is never blocked or spammed with failed
// (401) debits.
func MeteringConfigured() bool {
	return strings.TrimSpace(os.Getenv("COMMERCE_SERVICE_TOKEN")) != "" && NewMeteringClient("hanzo").Enabled()
}

// hourStamp is the per-hour bucket for the RequestID: the same running machine
// metered twice within one wall-clock hour carries the SAME RequestID (the client
// dedup hint). The authoritative once-per-hour guarantee is the single-flight
// lease in the ticker (object.ClaimMeterHour), because commerce does not dedup the
// withdraw on requestId.
func hourStamp(now time.Time) string { return now.UTC().Format("2006010215") }

// createdInHour reports whether an RFC3339 create time falls in the same UTC
// hour bucket as stamp ("YYYYMMDDHH"). An empty or unparseable time is NOT in the
// hour (metered normally) — we only ever SKIP a machine we can prove the launch
// path already billed this hour.
func createdInHour(createdTime, stamp string) bool {
	if createdTime == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, createdTime)
	if err != nil {
		return false
	}
	return t.UTC().Format("2006010215") == stamp
}

// MeterRunningMachines debits every RUNNING house resell machine one hour of its
// resale price to its OWNING org — the recurring counterpart to the launch debit.
// It is the "a running bound machine debits the org" rule: a machine that stays
// up keeps drawing down the org's credit balance, hour by hour.
//
// Per machine: org is recovered from the machine's own hanzo-org tag (never
// trusted from a client — it is the tag LaunchOrgMachine injected), the hourly
// price comes from the resale catalog (SizeBySlug → PriceToCents), and the debit
// carries RequestID "compute-<machineID>-<YYYYMMDDHH>". Recording is decoupled
// from gating (the machine already ran that hour, so the cost must be recorded);
// enforcement/suspend on a depleted balance is a separate control. A per-machine
// error is logged and does not abort the sweep.
//
// EXACTLY-ONCE PER HOUR is enforced OUTSIDE the RequestID: commerce's RecordUsage
// does NOT dedup the withdraw on requestId, so the key is only a reconciliation
// hint. The real once-per-hour guarantees are (1) the ticker's per-hour
// single-flight lease (object.ClaimMeterHour) so only one replica sweeps, and
// (2) skipping a machine's LAUNCH hour here (the launch path already billed it).
//
// No-op when metering is unconfigured or when compute is unconfigured (no house
// token) — nothing to enumerate, nothing to debit.
func MeterRunningMachines(ctx context.Context) {
	if !ComputeConfigured() || !MeteringConfigured() {
		return
	}
	machines, err := ListRunningHouseMachines()
	if err != nil {
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
// against a fake commerce.
func meterMachines(ctx context.Context, machines []*Machine, now time.Time) (metered, skipped int) {
	stamp := hourStamp(now)
	for _, m := range machines {
		org := orgFromTag(m.Tag)
		if org == "" {
			skipped++
			logs.Warning("compute metering: machine %s has no %s tag; skipping (unattributable)", m.Id, orgTagKey)
			continue
		}
		// Skip the LAUNCH hour: the launch path already debited this machine one
		// hour at create time (RequestID = machine id). Metering it again for the
		// same wall-clock hour would double-charge the launch hour (the launch and
		// sweep RequestIDs differ, and commerce does not dedup across them). A
		// machine with no parseable create time (older rows / test doubles) is
		// metered normally — a launch is never billed unless the launch path ran,
		// so failing to skip only risks the historical status quo, never a miss.
		if createdInHour(m.CreatedTime, stamp) {
			continue
		}
		si, err := SizeBySlug(m.Size)
		if err != nil || si == nil {
			skipped++
			logs.Warning("compute metering: size %q for machine %s not in catalog; skipping: %v", m.Size, m.Id, err)
			continue
		}
		cents := PriceToCents(si.PriceHourly)
		if cents <= 0 {
			continue // a $0 size is free — no debit (mirrors the launch path)
		}
		client := NewMeteringClient(org)
		if _, err := client.Record(ctx, metering.Usage{
			User:        org,
			Actor:       org,
			Org:         org,
			Currency:    "usd",
			AmountCents: cents,
			Provider:    meteringProvider,
			Model:       m.Size,
			Status:      "running",
			RequestID:   fmt.Sprintf("compute-%s-%s", m.Id, stamp),
		}); err != nil {
			skipped++
			logs.Warning("compute metering: debit machine %s (org %s): %v", m.Id, org, err)
			continue
		}
		metered++
	}
	return metered, skipped
}

// orgFromTag recovers the owning org from a machine's comma-joined tag string
// (as getMachineFromDroplet builds it): the value after "hanzo-org:". Empty when
// the machine carries no org tag — such a machine is unattributable and is
// skipped rather than billed to a wrong tenant.
func orgFromTag(tags string) string {
	want := orgTagKey + ":"
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, want) {
			return strings.TrimPrefix(t, want)
		}
	}
	return ""
}
