// Copyright 2023 Hanzo Industries Inc. All Rights Reserved.
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

package service

import (
	"errors"
	"testing"

	"github.com/hanzoai/money"
)

// TestGCPCellToCents pins how a BigQuery billing cell becomes cents. The conversion
// itself is money.ParseCents and is tested there; what is vm's own contract is which
// cells are spend, which are honestly zero, and which are refused.
//
// The table is the one three separate hand-written converters used to disagree on.
// The version this replaces was ParseFloat-then-*100 with `f <= 0 → 0`, so it got
// three families wrong: it dropped grouped amounts entirely ("1,234.56" → 0), it
// clamped credits to zero (hiding a refund), and it reported an unreadable cell as
// a confident $0 that no caller could distinguish from real zero spend.
func TestGCPCellToCents(t *testing.T) {
	cases := []struct {
		name string
		cell any
		want int64
	}{
		// ── ordinary spend, as BigQuery sends it: a decimal STRING ───────────
		{"ordinary amount", "12.34", 1234},
		{"cent-precise amount keeps its cent", "19.99", 1999},
		{"whole dollars", "100", 10000},
		{"zero", "0", 0},

		// ── the thousands separator, which used to zero the whole month ──────
		{"thousands separator", "1,234.56", 123456},
		{"grouped millions", "1,234,567.89", 123456789},

		// ── a credit is a refund and must stay negative ──────────────────────
		{"credit", "-5.00", -500},
		{"grouped credit", "-1,234.56", -123456},

		// ── a null SUM means no rows matched: honestly zero, not an error ────
		{"null cell is no spend", nil, 0},

		// ── a JSON number goes through the SAME converter, not a second *100 ─
		{"float cell", float64(12.34), 1234},
		{"negative float cell", float64(-5), -500},
		{"float cell, cent-precise", float64(19.99), 1999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gcpCellToCents(tc.cell)
			if err != nil {
				t.Fatalf("gcpCellToCents(%#v) errored: %v", tc.cell, err)
			}
			if got != tc.want {
				t.Fatalf("gcpCellToCents(%#v) = %d, want %d", tc.cell, got, tc.want)
			}
		})
	}
}

// TestGCPCellToCentsRefusesUnreadable is the half that used to be a silent zero.
// Zero is a legal amount, so a converter that maps garbage to it hands the caller a
// confident "GCP billed nothing" for a cell it could not read, and the spend leaves
// the report with nothing to notice.
func TestGCPCellToCentsRefusesUnreadable(t *testing.T) {
	for _, cell := range []any{
		"",               // present but empty — not the same as a null SUM
		"   ",            // whitespace
		"notanumber",     // not a number
		"12.34.56",       // not a decimal
		"$12.34",         // a symbol is not this converter's job
		"--5.00",         // malformed sign
		true,             // wrong type entirely
		[]any{"12.34"},   // ditto
		map[string]any{}, // ditto
	} {
		got, err := gcpCellToCents(cell)
		if err == nil {
			t.Fatalf("gcpCellToCents(%#v) = %d with no error — an unreadable cell became spend", cell, got)
		}
		if got != 0 {
			t.Fatalf("gcpCellToCents(%#v) = %d alongside an error, want 0", cell, got)
		}
	}
}

// TestCostConvertersShareOneImplementation states the property this change exists to
// create. vm used to own a private dollarStringToCents that answered differently from
// commerce's and ai's copies; now every cost reader in this package converts through
// money.ParseCents, so there is one answer to compare against.
func TestCostConvertersShareOneImplementation(t *testing.T) {
	for _, s := range []string{"19.99", "1,234.56", "-5.00", "0", "100"} {
		want, err := money.ParseCents(s)
		if err != nil {
			t.Fatalf("money.ParseCents(%q) errored: %v", s, err)
		}
		got, err := gcpCellToCents(s)
		if err != nil {
			t.Fatalf("gcpCellToCents(%q) errored: %v", s, err)
		}
		if got != want {
			t.Fatalf("gcpCellToCents(%q) = %d but money.ParseCents = %d — the converters have drifted apart again", s, got, want)
		}
	}

	// And an unreadable value is refused with the shared sentinel, so a caller can
	// tell a parse failure from any other cost-read failure.
	if _, err := gcpCellToCents("notanumber"); !errors.Is(err, money.ErrInvalidAmount) {
		t.Fatalf("gcpCellToCents error = %v, want it to match money.ErrInvalidAmount", err)
	}
}
