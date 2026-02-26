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

import "github.com/hanzoai/visor/util"

// DefaultPlans returns the seed catalog of resale VM plans.
// Provider mapping JSON encodes which provider/serverType/location to use per region.
//
// Customer-facing pricing is consistent regardless of backend provider.
// Routing: Hetzner for best margins, DO/Lightsail as premium regions.
//
// Regions: "us" (ash/hil), "eu" (fsn1/nbg1), "sg" (sin)
//
// Hetzner server types used:
//   Cost-optimized (CX):   cx22(2/4/40)
//   Shared Regular (CPX):  cpx11(2/2/40), cpx21(3/4/80), cpx31(4/8/160), cpx41(8/16/240)
//   Dedicated (CCX):       ccx13(2/8/80), ccx23(4/16/160), ccx33(8/32/240)
//
// Pricing aligned with hanzo/pricing cloudPlans:
//   nano=$5, bot=$10, standard=$15, pro=$25, power=$39, power-dedicated=$49
func DefaultPlans(owner string) []*Plan {
	now := util.GetCurrentTime()
	return []*Plan{
		{
			Owner: owner, Name: "nano", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Nano", Description: "Cheapest single VM. 1 vCPU, 1 GB RAM, 20 GB SSD.",
			Category: "nano", State: "Active",
			VCpu: 1, Ram: 1024, Disk: 20, CpuType: "shared",
			PriceMonthly: 500, // $5/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 512, // 500 GB
			SortOrder:    5,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cx22","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cx22","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cx22","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "bot", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Bot", Description: "Standard bot runner. 2 shared vCPU, 2 GB RAM, 40 GB SSD.",
			Category: "bot", State: "Active",
			VCpu: 2, Ram: 2048, Disk: 40, CpuType: "shared",
			PriceMonthly: 1000, // $10/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 1024, // 1 TB
			SortOrder:    10,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx11","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx11","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx12","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "standard", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Standard", Description: "Default plan. 2 vCPU, 8 GB RAM, 25 GB SSD. Up to 25 VMs.",
			Category: "standard", State: "Active",
			VCpu: 2, Ram: 8192, Disk: 25, CpuType: "shared",
			PriceMonthly: 1500, // $15/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 3072, // 3 TB
			SortOrder:    20,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx31","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx31","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx32","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "pro", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Pro", Description: "Dedicated CPU. 2 dedicated vCPU, 8 GB RAM, 80 GB SSD.",
			Category: "pro", State: "Active",
			VCpu: 2, Ram: 8192, Disk: 80, CpuType: "dedicated",
			PriceMonthly: 2500, // $25/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 2048, // 2 TB
			SortOrder:    30,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx13","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx13","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx13","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "power", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Power", Description: "High performance. 4 shared vCPU, 16 GB RAM, 160 GB SSD.",
			Category: "power", State: "Active",
			VCpu: 4, Ram: 16384, Disk: 160, CpuType: "shared",
			PriceMonthly: 3900, // $39/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 4096, // 4 TB
			SortOrder:    40,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx41","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx41","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx42","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "power-dedicated", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Power Dedicated", Description: "Dedicated high performance. 4 dedicated vCPU, 16 GB RAM, 160 GB SSD.",
			Category: "power", State: "Active",
			VCpu: 4, Ram: 16384, Disk: 160, CpuType: "dedicated",
			PriceMonthly: 4900, // $49/mo
			Regions:      "us,eu,sg",
			TrafficIncluded: 4096, // 4 TB
			SortOrder:    50,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx23","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx23","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx23","location":"sin"}
			}`,
		},
	}
}

// SeedDefaultPlans inserts default plans if none exist for the given owner.
func SeedDefaultPlans(owner string) error {
	plans, err := GetAllPlans(owner)
	if err != nil {
		return err
	}
	if len(plans) > 0 {
		return nil // already seeded
	}

	for _, plan := range DefaultPlans(owner) {
		_, err := AddPlan(plan)
		if err != nil {
			return err
		}
	}
	return nil
}
