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

import (
	"time"

	"xorm.io/core"
)

// CostCursor is the per-account watermark for BYOC cost billing (fleet tier a). It
// records how much of a BYOC provider's month-to-date cloud spend has ALREADY had
// the 1% fee applied, so each daily collector run meters only the INCREMENT since
// the last run — totaling 1% of the month's spend, idempotently. Month rollover is
// implicit in the PK: a new month starts a fresh cursor at zero, so the first run of
// a month bills 1% of that month's spend-to-date and no more.
//
// The daily BillingLease serializes the read-modify-write across replicas (only the
// lease winner advances the cursor), so this row only needs to persist the advancing
// watermark — no compare-and-set is required.
type CostCursor struct {
	Owner            string `xorm:"varchar(100) notnull pk" json:"owner"`
	Provider         string `xorm:"varchar(100) notnull pk" json:"provider"`
	Month            string `xorm:"varchar(6) notnull pk" json:"month"` // UTC "YYYYMM"
	BilledSpendCents int64  `json:"billedSpendCents"`                   // account MTD spend already 1%-billed
	UpdatedTime      string `xorm:"varchar(100)" json:"updatedTime"`
}

// AdvanceCostCursor advances the (owner, provider, month) watermark to the account's
// current month-to-date spend and returns the newly-billable spend INCREMENT (the
// month-to-date spend that had not yet been 1%-billed). It advances the watermark
// BEFORE the caller debits, so a debit failure UNDER-bills that increment — the safe
// direction for a paid product (a missed fee is reconcilable; a double debit is not).
// A zero/negative delta (no new spend, or a spend that only decreased via credits)
// advances nothing and returns 0.
func AdvanceCostCursor(owner, provider, month string, currentMTDCents int64) (int64, error) {
	cursor := CostCursor{Owner: owner, Provider: provider, Month: month}
	existed, err := Shared().Get(&cursor)
	if err != nil {
		return 0, err
	}

	delta := currentMTDCents - cursor.BilledSpendCents
	if delta <= 0 {
		return 0, nil
	}

	cursor.BilledSpendCents = currentMTDCents
	cursor.UpdatedTime = time.Now().UTC().Format(time.RFC3339)
	if existed {
		if _, err := Shared().ID(core.PK{owner, provider, month}).Cols("billed_spend_cents", "updated_time").Update(&cursor); err != nil {
			return 0, err
		}
	} else {
		if _, err := Shared().Insert(&cursor); err != nil {
			return 0, err
		}
	}
	// Ship the advanced watermark so it is durable BEFORE the caller debits the fee.
	// The BillingLease that gated this advance is already durable (shipped by
	// ClaimBillingUnit); shipping again here persists the cursor increment too, so a
	// post-flip owner both sees the unit claimed AND resumes from the advanced watermark
	// (never re-billing the increment). A ship failure surfaces so the caller can decide
	// (the lease row already blocks a same-unit re-bill).
	if err := pushShared(); err != nil {
		return 0, err
	}
	return delta, nil
}
