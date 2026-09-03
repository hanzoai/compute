// Copyright 2026 Hanzo Industries Inc. All Rights Reserved.
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

import "testing"

// A cluster create prices its seed pool through HourlyCents, so an EKS or GKE
// worker type must resolve to a real per-hour price — the create used to fail
// closed because those types are not DigitalOcean slugs. The price is the
// upstream cost marked up by the standard resale margin, resolved without a
// network call (no catalog seed here on purpose).
func TestHourlyCents_PricesEKSAndGKE(t *testing.T) {
	cases := []struct {
		slug string
		cost float64 // upstream on-demand hourly, pre-markup
	}{
		{"m5.large", 0.096},         // EKS
		{"e2-standard-4", 0.134012}, // GKE
	}
	for _, tc := range cases {
		cents, err := HourlyCents(tc.slug)
		if err != nil {
			t.Fatalf("HourlyCents(%q) must price a managed-K8s worker type, got %v", tc.slug, err)
		}
		want := PriceToCents(HanzoPrice(tc.cost, false))
		if cents != want {
			t.Fatalf("HourlyCents(%q) = %d, want %d (cost %.6f × resale margin)", tc.slug, cents, want, tc.cost)
		}
		if cents <= 0 {
			t.Fatalf("HourlyCents(%q) = %d, a priced type must never be free", tc.slug, cents)
		}
	}
}
