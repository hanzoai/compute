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

	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/billing"
	"github.com/hanzoai/visor/conf"
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

	// billing: report node pool usage every hour.
	//
	// Wired UNCONDITIONALLY, and the reporter is built per tick. Both halves
	// matter: a startup-time config check silently disables billing FOREVER for
	// a pod that booted before its KMS-synced credential landed, and a reporter
	// built once captures whatever the config said at boot. Deciding per tick
	// means a late credential starts billing on the next hour, and a missing one
	// is reported every hour instead of never (ReportAllNodePools is loud).
	billingTicker := time.NewTicker(time.Hour)
	go func() {
		for range billingTicker.C {
			billing.NewBillingReporter(
				conf.GetConfigString("commerceUrl"),
				conf.GetConfigString("commerceToken"),
			).ReportAllNodePools(context.Background())
		}
	}()
	logs.Info("billing: hourly node pool usage reporting enabled")

	// compute metering: debit every RUNNING /v1 resell machine one hour of its
	// resale price to its owning org, every hour, on the canonical commerce/
	// metering path (same client the launch debit uses). A running bound machine
	// keeps drawing down the org's credit balance. Enablement is decided inside
	// MeterRunningMachines (no-op when commerce or compute is unconfigured), so
	// this is wired unconditionally — no second config gate to drift.
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
			if object.IsBillingOwner() && object.ClaimMeterHour(time.Now()) {
				service.MeterRunningMachines(context.Background())
			}
		}
	}()
	logs.Info("compute metering: hourly running-machine drawdown enabled (single-flight per hour, elected owner)")
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
