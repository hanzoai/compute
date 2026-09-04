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

package object

// coordinator.go answers ONE money-safety question, identically on every replica:
// "may THIS replica claim a billing lease right now?" It is the single-writer gate
// that makes hourly metering EXACTLY-ONCE under the multi-replica compute Deployment.
//
// WHY THIS EXISTS. The billing leases (MeterLease, BillingLease, CostCursor) live on
// the `_global` coordination DB. Under the Base backend that DB is a POD-LOCAL SQLite
// file, so two replicas each hold their OWN copy: an unguarded insert-once lease wins
// on BOTH files (independent primary-key spaces) and every running machine is debited
// twice an hour. The fix is exactly-once as TWO composed guarantees:
//
//	(1) single-writer gate  — only the HRW-elected owner of "_global" runs the metering
//	    sweep AND performs lease claims; every other replica skips (this file).
//	(2) durable lease history across handoff — a new owner Pulls the prior owner's lease
//	    rows from the object store BEFORE it can re-claim, so the insert-once PK holds
//	    GLOBALLY, not per-pod (store_base.go PullSharedLeases/PushShared, driven by the
//	    claim path in meter_lease.go / billing_lease.go).
//
// The election is the SAME deterministic HRW (Rendezvous) primitive every Hanzo
// singleton uses (github.com/hanzoai/ha) — no coordinator, no lock service, no
// Postgres, no Redis. It is applied to the single coordination key `_global`, so
// every replica computes the same owner from the same live membership set.
//
// WHERE THE MEMBERSHIP COMES FROM. Election needs the live replica set, and this
// binary does not link a Kubernetes client to get it: compute is the MULTI-CLOUD VM
// path, and cluster access belongs to the cluster controllers. So membership is a
// registration seam over ha.Membership — the interface hanzoai/ha already defines for
// exactly this, reused rather than re-declared. An operator that can enumerate
// replicas installs a source with RegisterMembership; nothing else changes.
//
// FAIL-CLOSED. For a paid product the safe failure is NOT to bill (a missed hour is
// reconciled; a double debit is not). Any uncertainty about ownership — an empty or
// unreadable membership set, or no source registered at all — yields "not owner", so
// the replica skips rather than risk a duplicate debit.

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/compute/logs"
)

// coordKey is the single coordination resource every replica elects an owner for:
// the `_global` coord DB that holds every billing lease. HRW over this one key names
// exactly one billing owner cluster-wide.
const coordKey = "_global"

// membershipTimeout bounds the peer lookup the ownership check makes. The sweep is
// hourly, so a tight timeout costs nothing on the hot path and never wedges a tick.
const membershipTimeout = 5 * time.Second

var (
	membershipMu sync.RWMutex
	// membership is the registered replica-set source, or nil for the unavailable
	// default. Tests swap it directly to force owner / non-owner deterministically.
	membership ha.Membership
)

// RegisterMembership installs the live replica-set source election runs over. It is
// the ONE seam by which a deployment that can enumerate compute's replicas (a cluster
// controller, an operator sidecar) teaches this process who its peers are. A
// single-process deployment registers ha.Static(id): the sole process is the sole
// writer, so it correctly elects itself.
func RegisterMembership(m ha.Membership) {
	membershipMu.Lock()
	defer membershipMu.Unlock()
	membership = m
}

// BuildMembership returns the registered source, or the unavailable default. It never
// returns nil: a nil source would push a nil-check onto every call site, and the one
// that got forgotten would panic inside a billing tick instead of skipping the hour.
func BuildMembership() ha.Membership {
	membershipMu.RLock()
	m := membership
	membershipMu.RUnlock()
	if m == nil {
		return unavailableMembership{reason: "no replica-set source registered " +
			"(this build links no Kubernetes client); the billing owner cannot be " +
			"elected, so lease claims and metering debits stay disabled"}
	}
	return m
}

// billingOwner reports whether THIS replica is the elected single writer for the
// billing coord DB right now. It is the ONE predicate the ticker gate and every lease
// claim share, so there is exactly one notion of "am I allowed to bill".
//
// Fail-closed: if membership cannot be read, or the live set is empty, this returns
// false and the caller skips (no claim, no debit). The only "true" is a confident
// HRW win over a known, non-empty membership set.
func billingOwner() bool {
	m := BuildMembership()
	ctx, cancel := context.WithTimeout(context.Background(), membershipTimeout)
	defer cancel()
	members, err := m.Members(ctx)
	if err != nil || len(members) == 0 {
		return false // unknown membership → fail closed (never risk a double debit).
	}
	return ha.IsOwner(coordKey, m.Self(), members)
}

// IsBillingOwner is the exported ownership gate the metering ticker checks before it
// arms an hourly sweep, so non-owner replicas never even enumerate machines or call
// commerce. It is an optimization layered over the authoritative per-claim gate inside
// ClaimMeterHour / ClaimBillingUnit — both consult the SAME predicate, so the ticker
// gate can never admit a claim the lease path would refuse.
func IsBillingOwner() bool { return billingOwner() }

// unavailableMembership is the no-source default. It reports an ERROR rather than an
// empty set on purpose: an empty set would read as the positive claim "I have no
// peers", which is exactly the lie that double-debits a two-replica Deployment.
// "I do not know who my peers are" is a different answer, and only the error carries it.
type unavailableMembership struct{ reason string }

// unavailableOnce keeps the warning to one line per process. Refusing to bill must be
// visible — a silent revenue outage is worse than a loud one — but the gate is
// consulted every hour and by every claim, so it says so once.
var unavailableOnce sync.Once

func (u unavailableMembership) Self() string { return SelfID() }

func (u unavailableMembership) Members(context.Context) ([]ha.Member, error) {
	unavailableOnce.Do(func() { logs.Warning("compute billing: %s", u.reason) })
	return nil, &membershipUnavailableError{reason: u.reason}
}

// membershipUnavailableError marks "the replica set is unknown", never "the replica
// set is empty". Callers fail closed on both, but only this one means a source is
// missing, and an operator reading the log needs to be able to tell them apart.
type membershipUnavailableError struct{ reason string }

func (e *membershipUnavailableError) Error() string {
	return "compute: replica membership unavailable: " + e.reason
}

// SelfID is this replica's stable identity for HRW weighting: POD_NAME (Downward API)
// when set, else the container hostname (which is the pod name in a Deployment pod).
// A stable, unique-per-replica string is all HRW needs.
//
// Exported so the composition root can build ha.Static from the SAME identity the
// election weighs. Two spellings of "who am I" is one spelling too many.
func SelfID() string {
	if n := strings.TrimSpace(os.Getenv("POD_NAME")); n != "" {
		return n
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "self" // last-resort constant: in single-process mode the value only
		// has to be stable within the process, and there is one member.
	}
	return h
}
