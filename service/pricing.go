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

package service

import "math"

// Resell markup — the ONE place Hanzo's compute margin over DigitalOcean's
// list price is defined. DigitalOcean is the wholesale cost; Hanzo resells at
// list * markup. GPU compute carries a gentler multiplier (already high
// absolute cost, thinner competitive margin); everything else uses the base.
//
// Keep this as the single source of truth for compute pricing. If pricing ever
// needs per-SKU tiers, extend HanzoPrice here — never scatter markups across
// controllers or the dashboard.
const (
	resellMarkupBase = 1.40
	resellMarkupGPU  = 1.25
)

// HanzoPrice converts a DigitalOcean list price (USD) into Hanzo's resale
// price (USD). isGPU selects the GPU multiplier. Rounded to 5 decimals so
// hourly micro-prices (e.g. $0.00744/hr) survive while monthly stays clean.
func HanzoPrice(doPrice float64, isGPU bool) float64 {
	m := resellMarkupBase
	if isGPU {
		m = resellMarkupGPU
	}
	return math.Round(doPrice*m*1e5) / 1e5
}
