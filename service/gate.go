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

// gate.go is the compute money gate: ONE price resolver, ONE pre-provision
// balance check, ONE debit. Every compute resource visor provisions rides
// through it — droplet launch, DOKS cluster create, node pool create, node pool
// scale — so there is a single answer to "what does this cost?" and a single
// answer to "is this org good for it?".
//
// Two invariants, and both halves matter:
//
//	nothing provisions before its org is authorized for the first interval, and
//	nothing provisions at a price of zero.
//
// A gate that authorizes a $0 charge is not a gate. An unpriced GPU slug billed
// as free is the same leak as no gate at all, reached by a different road.
package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/visor/logs"
	"github.com/hanzoai/visor/telemetry"
)

// ErrPriceUnavailable reports that a size's price could not be RESOLVED. It is
// deliberately distinct from a resolved price of zero, because collapsing the two
// is how an H100 pool bills nothing: a slug missing from the catalog reads as
// "free" and provisions at $0/hr for as long as it runs. Callers refuse to
// provision on this error — they never fall back to zero.
var ErrPriceUnavailable = errors.New("price unavailable")

// HourlyCents resolves a size slug to Hanzo's resale price in cents per hour,
// from the SAME catalog the POST /v1/machines quote and debit read
// (SizeBySlug → PriceToCents). There is no second price table.
//
// Both a slug absent from the catalog AND a slug whose resolved price is <= 0
// yield ErrPriceUnavailable. The upstream publishes no zero-priced size, so a
// zero here means the price did not resolve — calling that "free by policy" is
// precisely the mistake this function exists to prevent. A genuinely free SKU
// would be a catalog decision, expressed in the catalog, not an absence.
func HourlyCents(slug string) (int64, error) {
	si, err := SizeBySlug(slug)
	if err != nil {
		return 0, fmt.Errorf("%w: resolving size %q: %v", ErrPriceUnavailable, slug, err)
	}
	if si == nil {
		return 0, fmt.Errorf("%w: size %q is not in the catalog", ErrPriceUnavailable, slug)
	}
	return RateOf(si)
}

// RateOf is the price half of HourlyCents, for a size the caller has ALREADY
// resolved from the catalog — which the launch path has, since it resolved the
// size to quote it. There is one refuse-rather-than-zero rule and this is it;
// HourlyCents is the slug half in front of it, not a second copy.
func RateOf(si *SizeInfo) (int64, error) {
	if si == nil {
		return 0, fmt.Errorf("%w: size is not in the catalog", ErrPriceUnavailable)
	}
	cents := PriceToCents(si.PriceHourly)
	if cents <= 0 {
		return 0, fmt.Errorf("%w: size %q resolved to a zero price", ErrPriceUnavailable, si.Slug)
	}
	return cents, nil
}

// AuthorizeCompute is the pre-provision balance gate, and it is FAIL-CLOSED:
// every non-nil answer from commerce refuses the provision — insufficient funds,
// 401 on a rotated service token, 5xx, timeout, unreachable. Availability does
// not outrank billing here, because the resource on the other side of this call
// costs real money every hour it stays up, and nobody notices an unbilled GPU
// until the upstream invoice arrives.
//
// cents is the FIRST INTERVAL's FULL cost (hourly × node count), never a token
// amount, so a one-cent balance cannot green-light an eight-GPU pool.
//
// The balance consulted is PREPAID ONLY — see NewMeteringClient. Project scopes
// the tenant's own spend cap; the balance debited is always the org's.
func AuthorizeCompute(ctx context.Context, org, project string, cents int64) error {
	if cents <= 0 {
		return fmt.Errorf("%w: refusing to authorize a zero charge", ErrPriceUnavailable)
	}
	err := NewMeteringClient(org).Authorize(ctx, metering.AuthInput{
		User:        org,
		Actor:       MeterActor(org, project),
		Org:         org,
		Currency:    "usd",
		AmountCents: cents,
		Project:     project,
		Service:     meteringProvider,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, metering.ErrInsufficientBalance):
		return fmt.Errorf("insufficient balance: %d cents required for the first hour", cents)
	case errors.Is(err, metering.ErrSpendCapExceeded):
		return fmt.Errorf("spend cap exceeded: %d cents required for the first hour", cents)
	default:
		return fmt.Errorf("billing authorization failed: %v", err)
	}
}

// RecordCompute debits cents to the org's commerce ledger on the ONE compute
// meter line — Provider "compute", attributed to org+project through the shared
// MeterActor — and mirrors it as an OTel metric.
//
// model is the size slug, status the lifecycle point ("launched"/"running"), and
// requestID the idempotency hint. Commerce does NOT dedup on requestID, so the
// once-per-unit guarantee belongs to the caller (the hour lease for sweeps, the
// launch-hour skip for anything the provision path already billed).
func RecordCompute(ctx context.Context, org, project string, cents int64, model, status, requestID string) error {
	if cents <= 0 {
		return nil
	}
	if _, err := NewMeteringClient(org).Record(ctx, metering.Usage{
		User:        org,
		Actor:       MeterActor(org, project),
		Org:         org,
		Currency:    "usd",
		AmountCents: cents,
		Provider:    meteringProvider,
		Model:       model,
		Status:      status,
		RequestID:   requestID,
	}); err != nil {
		return err
	}
	telemetry.CountMetered(ctx, org, project, meteringProvider, cents)
	return nil
}

// ---- the provisioning lease ----
//
// AuthorizeCompute is a READ. Sixteen requests can read the same balance in the
// window before any of them writes a debit, and every one of them is told yes —
// measured: an org funded for one cluster-hour provisioned sixteen, and finished
// the second at minus $476. A gate that every concurrent caller passes is a
// speed bump.
//
// The hold closes the window by making the sequence atomic per org: a balance one
// request clears is a balance the next request no longer sees, because the debit
// lands before the next read. It is per ORG, so one tenant's provision never
// waits on another's.
//
// It is a PROCESS lease, and that is exactly as wide as visor's money safety
// already is: the hourly meter's exactly-once rests on this Deployment running
// `replicas: 1` (object/coordinator.go's ha.Static, paired with the replica count
// in universe charts/app/values/hanzo/visor.yaml), because under the Base backend
// the shared coord is pod-local and no insert-once PK spans pods. Raising
// replicas requires a real membership source AND a cross-pod hold, in the same
// change — the same coupling, stated in the same terms, for the same reason.

type orgHold struct {
	mu      sync.Mutex
	waiting int
}

var (
	holdsMu sync.Mutex
	holds   = map[string]*orgHold{}
)

// holdOrg takes org's provisioning lease and returns its release. The map is
// pruned when the last waiter leaves, so a tenant list does not become a leak.
func holdOrg(org string) func() {
	holdsMu.Lock()
	h := holds[org]
	if h == nil {
		h = &orgHold{}
		holds[org] = h
	}
	h.waiting++
	holdsMu.Unlock()

	h.mu.Lock()
	return func() {
		h.mu.Unlock()
		holdsMu.Lock()
		h.waiting--
		if h.waiting == 0 {
			delete(holds, org)
		}
		holdsMu.Unlock()
	}
}

// Provision is the ONE metered provision, and every compute resource visor
// creates rides it: a droplet launch, a DOKS cluster, a node pool, a scale-up.
// Under the org's hold it authorizes, provisions, and debits — in that order, so
// the money moves before the next request reads the balance.
//
// TWO amounts, because they answer two different questions, and answering both
// with one number is a bug in whichever direction the number leans:
//
//	authorize — the CEILING the org must be good for. For an autoscaling pool
//	            that is the quantity it is ALLOWED to reach, because nothing
//	            gates the growth: the upstream adds nodes on its own and no
//	            request reaches visor, so the balance has to cover the ceiling
//	            up front or it never gets asked again.
//	debit     — what the org actually OWES right now, which is the resource it
//	            actually got. Charging the ceiling here bills sixteen nodes for
//	            a pool that has one, at hour one, forever after refunded by
//	            nobody. The other fifteen become chargeable when the upstream
//	            actually adds them, and the hourly sweep is what charges them.
//
// model is the size slug. provision does the work and returns the idempotency key
// naming what it provisioned — or "" when the provision carries no charge of its
// OWN, which is the scale path: the added nodes are billed by the recurring sweep
// from its next hour, and a debit here would overlap that same hour's sweep.
//
// A failed provision debits nothing. A failed DEBIT does not un-provision — the
// resource exists and is drawing upstream cost, and refusing to hand it over
// would leave the customer paying for something they were told they did not get —
// but it is loud, because nothing reconciles it.
func Provision(ctx context.Context, org, project string, authorize, debit int64, model string, provision func() (string, error)) error {
	defer holdOrg(org)()

	if err := AuthorizeCompute(ctx, org, project, authorize); err != nil {
		return err
	}
	requestID, err := provision()
	if err != nil {
		return err
	}
	if requestID == "" {
		return nil
	}
	if err := RecordCompute(ctx, org, project, debit, model, "launched", requestID); err != nil {
		logs.Warning("compute metering: debit %s (org %s, %d cents): %v", requestID, org, debit, err)
	}
	return nil
}

// Billable reports whether a billing sweep can actually debit — and makes a NO
// loud. An absent or rotated COMMERCE_SERVICE_TOKEN stops ALL revenue collection
// while the machines keep running and keep costing us money upstream, so the
// silent early-return this replaces was a revenue outage wearing the uniform of a
// healthy service: no error, no log, no metric, green dashboards, zero invoices.
//
// sweep names the caller ("compute.hourly", "pool.hourly", …) so
// "the sweep did not run this hour" is alertable on one metric series.
func Billable(ctx context.Context, sweep string) bool {
	if MeteringConfigured() {
		return true
	}
	logs.Warning("billing: %s sweep SKIPPED — COMMERCE_SERVICE_TOKEN is unset or empty, so running resources are NOT being billed", sweep)
	telemetry.CountBillingSkip(ctx, sweep)
	return false
}
