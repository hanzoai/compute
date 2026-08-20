// Copyright 2024 Hanzo Industries Inc. All Rights Reserved.
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

package task

import (
	"context"
	"time"

	"github.com/hanzoai/visor/billing"
	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/object"
	"github.com/hanzoai/visor/service"
)

type Ticker struct{}

func NewTicker() *Ticker {
	return &Ticker{}
}

func (t *Ticker) SetupTicker() {
	// delete unused session every hour
	unUsedSessionTicker := time.NewTicker(time.Hour)
	go func() {
		for range unUsedSessionTicker.C {
			t.deleteUnUsedSession()
		}
	}()

	// compute metering: debit every RUNNING resell resource one hour of its resale
	// price to its owning org, every hour, on the canonical commerce/metering path
	// (same client the launch debit uses). A running machine and a running node
	// pool both keep drawing down the org's credit balance.
	//
	// ONE sweep, TWO resource kinds, ONE lease. Machines are enumerated from the
	// house DigitalOcean account and node pools from the store, but they are the
	// same billable hour, so they share the per-hour claim below. Two tickers each
	// calling ClaimMeterHour would have one starve the other: the hour is a single
	// PK, so whichever claimed first would win it and the other kind would never be
	// billed at all. (Node pools used to run on a second, unleased ticker — so
	// every replica swept them, and nothing stopped a rolling deploy re-billing an
	// hour.)
	//
	// Enablement is decided inside each sweep (no-op when commerce or compute is
	// unconfigured), so this is wired unconditionally — no second config gate to
	// drift.
	//
	// SINGLE-FLIGHT ACROSS REPLICAS (money safety): visor runs replicas: 2+ with no
	// external coordinator, and commerce does NOT dedup the withdraw on requestId, so an
	// unguarded sweep would double-debit every machine every hour. Exactly-once is TWO
	// composed guarantees, both inside object:
	//   (1) IsBillingOwner() — only the HRW-elected owner of the `_global` coord DB runs
	//       the sweep, so non-owner replicas never enumerate machines or call commerce
	//       (they still serve all read/stateless traffic — only the money-WRITER is gated).
	//   (2) ClaimMeterHour — the per-hour lease (hydrate prior claims, insert-once, ship
	//       synchronously) makes exactly ONE claim win per wall-clock hour cluster-wide,
	//       and blocks a mid-hour restart or a post-flip new owner from re-sweeping.
	// At replicas: 1 the sole pod always elects itself owner (no external coordinator
	// needed), so it bills normally; at replicas: N only the elected owner bills.
	// ClaimMeterHour re-checks ownership, so the gate here is a clean-skip optimization,
	// never the sole guarantee.
	//
	// Wired UNCONDITIONALLY — enablement is decided INSIDE MeterRunningMachines
	// every hour, which is what the paragraph above already promised. The
	// startup-time MeteringConfigured() check that used to stand here broke that
	// promise in the worst direction: a pod that booted before its KMS-synced
	// COMMERCE_SERVICE_TOKEN landed never created the ticker at all, so it never
	// billed a single machine-hour for the life of the pod, silently.
	//
	// The tick body is hour.run, which adds the PRECONDITION on claiming — the
	// provider must answer before the hour is spent. Those two exactly-once
	// guarantees are untouched by it; see run for why a read-only probe cannot
	// weaken them.
	computeTicker := time.NewTicker(time.Hour)
	go func() {
		for range computeTicker.C {
			liveHour().run(context.Background(), time.Now())
		}
	}()
	logs.Info("compute metering: hourly running-resource drawdown enabled (machines + node pools, single-flight per hour, elected owner)")
}

// hour is one hourly tick's four collaborators, named so the ORDER they compose
// in is a thing a test can hold still. The order is the whole correctness
// argument — see run — and an order is not testable while it is spelled inline in
// a goroutine that only fires on the top of the hour.
type hour struct {
	// owner reports whether this replica is the elected single writer.
	owner func() bool
	// reachable proves the provider actually answers, or says why not.
	reachable func(context.Context) error
	// claim consumes the hour, cluster-wide, exactly once.
	claim func(time.Time) bool
	// meter does the billing work the claim paid for.
	meter func(context.Context, time.Time)
}

// liveHour binds the production collaborators. It is the only place the real
// ones are named, so the test wires fakes into the same shape rather than a
// parallel copy of the logic.
func liveHour() hour {
	return hour{
		owner:     object.IsBillingOwner,
		reachable: service.ComputeReachable,
		claim:     object.ClaimMeterHour,
		meter: func(ctx context.Context, now time.Time) {
			service.MeterRunningMachines(ctx)
			billing.MeterRunningNodePools(ctx, now)
		},
	}
}

// run performs ONE hourly tick: prove, then claim, then bill.
//
// PROVE BEFORE CLAIM is the ordering that matters, and it used to be the other
// way round. The claim is insert-once on the wall-clock hour with no retry and
// no reconciliation, so it does not record "this hour was billed" — it records
// "this hour is spent, and no one may ever bill it again". Spending it before
// knowing whether the work is possible means an unreachable provider burns the
// hour: both sweeps bill nothing, log a warning, report a healthy `sweep done
// (metered=0)`, and the revenue for that hour is not delayed but destroyed. A
// credential that comes back cannot recover it, because the hour is claimed.
//
// So the hour is now claimed only once the provider has answered. An hour we
// cannot bill is left UNCLAIMED, which is the difference between a missed hour
// and a lost one: the lease stays free, so the hour remains billable the moment a
// credential returns. Every fail-closed comment in the billing path already
// promised "a missed hour is reconciled" — leaving the lease unclaimed is what
// makes that promise reachable instead of merely stated.
//
// EXACTLY-ONCE IS UNCHANGED, and moving the probe cannot weaken it. The probe is
// a read: it moves no money, so running it on every replica costs nothing and
// proves nothing about who bills. The claim is still the sole authority on that,
// still insert-once on the same hour PK over the same HRW-elected single writer
// (object.ClaimMeterHour), and still the only thing standing between meter and a
// duplicate debit. Two replicas that both prove the provider reachable still race
// exactly one claim, and exactly one of them wins it. Nothing here is a second
// mechanism; the probe only decides whether the existing one is asked.
//
// An UNCONFIGURED house account is not unreachable — ComputeReachable returns nil
// with nothing to ask — so a deployment with no house token bills its tenants'
// own resources exactly as before.
func (h hour) run(ctx context.Context, now time.Time) {
	if !h.owner() {
		return
	}
	if err := h.reachable(ctx); err != nil {
		// Loud: this is a revenue outage, and the sweep it replaces reported
		// success. The hour is deliberately left unclaimed — see above.
		logs.Warning("compute metering: hour %s NOT claimed — the provider is unreachable (%v); "+
			"nothing is billed and nothing is spent, so this hour stays billable once a credential returns",
			now.UTC().Format("2006010215"), err)
		return
	}
	if !h.claim(now) {
		return
	}
	h.meter(ctx, now)
}

func (t *Ticker) deleteUnUsedSession() {
	sessions, err := object.GetSessionsByStatus([]string{object.NoConnect, object.Connecting})
	if err != nil {
		logs.Info("failed to get unused sessions: ", err)
		return
	}

	now := time.Now()
	for _, session := range sessions {
		if session.StartTime != "" {
			startTime, err := time.ParseInLocation(time.RFC3339, session.StartTime, time.Local)
			if err != nil {
				continue
			}
			if now.Sub(startTime).Hours() > 1 {
				_, err := object.DeleteSessionById(session.GetId())
				if err != nil {
					logs.Info("delete session failed: %v", err)
					return
				}
			}
		} else {
			_, err := object.DeleteSessionById(session.GetId())
			if err != nil {
				logs.Info("delete session failed: %v", err)
				return
			}
		}
	}
}
