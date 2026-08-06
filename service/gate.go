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
// from the SAME catalog the /v1/machines/launch quote and debit read
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
	cents := PriceToCents(si.PriceHourly)
	if cents <= 0 {
		return 0, fmt.Errorf("%w: size %q resolved to a zero price", ErrPriceUnavailable, slug)
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
