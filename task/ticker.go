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
	computeTicker := time.NewTicker(time.Hour)
	go func() {
		for range computeTicker.C {
			now := time.Now()
			if !object.IsBillingOwner() || !object.ClaimMeterHour(now) {
				continue
			}
			ctx := context.Background()
			service.MeterRunningMachines(ctx)
			billing.MeterRunningNodePools(ctx, now)
		}
	}()
	logs.Info("compute metering: hourly running-resource drawdown enabled (machines + node pools, single-flight per hour, elected owner)")
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
