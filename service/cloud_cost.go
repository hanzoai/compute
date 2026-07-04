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

// cloud_cost.go is the ONE seam for reading a BYOC cloud account's spend — the
// input to fleet-billing tier (a), where connected cloud accounts are billed 1% of
// their cloud spend. Each cloud implements CloudCostReader with that cloud's real
// cost API (AWS Cost Explorer, Azure Cost Management, GCP BigQuery billing export,
// DigitalOcean billing). Readers are STATELESS pure API reads (this package must
// not import object — object imports service); the incremental "bill only new
// spend" watermark + idempotency live in the billing orchestrator, which owns the
// persistent cursor.
//
// HONESTY CONTRACT: a reader returns ErrCostUnavailable when the stored credentials
// lack the cost-read scope that cloud needs (documented per cloud). The collector
// then SKIPS that account — no fee — rather than inventing a number. No spend is
// ever fabricated.
package service

import (
	"context"
	"errors"
	"time"
)

// ErrCostUnavailable means this provider's spend cannot be read with the configured
// credentials/scope. The collector skips the provider (no fee); it is NOT an error
// condition to alert on, just "cost-read not wired for this account".
var ErrCostUnavailable = errors.New("cloud cost: unavailable (cost-read not configured for this provider)")

// CloudCostReader reads a BYOC cloud account's cumulative spend for the current UTC
// calendar month, in whole cents of the account's billing currency. Month-to-date
// is the ONE figure every cloud can report uniformly (a per-day figure is not
// available from every cloud, e.g. DigitalOcean's balance endpoint), so the billing
// orchestrator bills 1% of the INCREMENT since it last billed — totaling 1% of the
// month's spend, idempotently, with month-rollover handled by the cursor key.
type CloudCostReader interface {
	// MonthToDateCents returns the account's spend so far this UTC month, in cents.
	// Zero is valid (no spend → no fee). ErrCostUnavailable (or any error) makes the
	// collector skip this account for this run — never fabricating a figure.
	MonthToDateCents(ctx context.Context, now time.Time) (int64, error)
}

// NewCostReader builds the cost reader for a BYOC provider from its stored creds.
// providerType is the internal provider type; (clientId, clientSecret, region) is
// the managed-machine credential triple; costScope is Provider.CostReadScope (the
// cloud-specific extra identifier the cost API needs). Returns ErrCostUnavailable
// for a provider type with no cost reader, so the collector skips it honestly. The
// internal provider names never leave this package (brand policy).
func NewCostReader(providerType, clientId, clientSecret, region, costScope string) (CloudCostReader, error) {
	switch providerType {
	case "AWS":
		return newAWSCostReader(clientId, clientSecret, region)
	case "DigitalOcean":
		return newDOCostReader(clientId, clientSecret)
	case "Azure":
		return newAzureCostReader(clientId, clientSecret, costScope)
	case "Google Cloud":
		return newGCPCostReader(clientSecret, costScope)
	default:
		return nil, ErrCostUnavailable
	}
}

// monthStart returns 00:00:00 UTC on the first day of now's month — the lower bound
// of a month-to-date cost query, shared by the readers that take an explicit range.
func monthStart(now time.Time) time.Time {
	y, m, _ := now.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}
