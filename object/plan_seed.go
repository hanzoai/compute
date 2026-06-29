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

import "github.com/hanzoai/vm/util"

// DefaultPlans returns the Hanzo Cloud plan catalog.
// Provider mapping is internal — customers never see backend provider names.
//
// Regions: "us" (ash/hil), "eu" (fsn1/nbg1), "sg" (sin)
// Pricing aligned with hanzo/pricing cloudPlans.
func DefaultPlans(owner string) []*Plan {
	now := util.GetCurrentTime()
	return []*Plan{
		{
			Owner: owner, Name: "starter", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Starter", Description: "Get started for free. 1 vCPU, 1 GB RAM, 20 GB SSD.",
			Category: "starter", State: "Active",
			VCpu: 1, Ram: 1024, Disk: 20, CpuType: "shared",
			PriceMonthly:    500, // $5/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 512,
			SortOrder:       5,
			ProviderMapping: `{
				"us":{"provider":"Lightsail","serverType":"nano_3_0","location":"us-east-1"},
				"eu":{"provider":"Hetzner","serverType":"cx22","location":"fsn1"},
				"sg":{"provider":"Lightsail","serverType":"nano_3_0","location":"ap-southeast-1"}
			}`,
		},
		{
			Owner: owner, Name: "builder", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Builder", Description: "For developers shipping real products. 2 vCPU, 2 GB RAM, 40 GB SSD.",
			Category: "builder", State: "Active",
			VCpu: 2, Ram: 2048, Disk: 40, CpuType: "shared",
			PriceMonthly:    1000, // $10/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 1024,
			SortOrder:       10,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx11","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx11","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx12","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "dev", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Dev", Description: "The sweet spot. 2 vCPU, 8 GB RAM, 25 GB SSD.",
			Category: "dev", State: "Active",
			VCpu: 2, Ram: 8192, Disk: 25, CpuType: "shared",
			PriceMonthly:    1500, // $15/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 3072,
			SortOrder:       20,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx31","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx31","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx32","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "pro", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Pro", Description: "Dedicated CPU. Zero noisy neighbors. 2 dedicated vCPU, 8 GB RAM, 80 GB SSD.",
			Category: "pro", State: "Active",
			VCpu: 2, Ram: 8192, Disk: 80, CpuType: "dedicated",
			PriceMonthly:    2500, // $25/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 2048,
			SortOrder:       30,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx13","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx13","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx13","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "turbo", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Turbo", Description: "4x the power. 4 vCPU, 16 GB RAM, 160 GB SSD.",
			Category: "turbo", State: "Active",
			VCpu: 4, Ram: 16384, Disk: 160, CpuType: "shared",
			PriceMonthly:    3900, // $39/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 4096,
			SortOrder:       40,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"cpx41","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"cpx41","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"cpx42","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "turbo-dedicated", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Turbo Dedicated", Description: "All the power of Turbo with dedicated cores. 4 dedicated vCPU, 16 GB RAM, 160 GB SSD.",
			Category: "turbo", State: "Active",
			VCpu: 4, Ram: 16384, Disk: 160, CpuType: "dedicated",
			PriceMonthly:    4900, // $49/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 4096,
			SortOrder:       50,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx23","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx23","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx23","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "business", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Business", Description: "Team-scale compute. 8 dedicated vCPU, 32 GB RAM, 240 GB SSD.",
			Category: "business", State: "Active",
			VCpu: 8, Ram: 32768, Disk: 240, CpuType: "dedicated",
			PriceMonthly:    21900, // $219/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 20480,
			SortOrder:       60,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx33","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx33","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx33","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "enterprise", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Enterprise", Description: "Mission-critical infrastructure. 16 dedicated vCPU, 64 GB RAM, 360 GB SSD.",
			Category: "enterprise", State: "Active",
			VCpu: 16, Ram: 65536, Disk: 360, CpuType: "dedicated",
			PriceMonthly:    42900, // $429/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 40960,
			SortOrder:       70,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx43","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx43","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx43","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "scale", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Scale", Description: "Platform-scale compute. 32 dedicated vCPU, 128 GB RAM, 600 GB SSD.",
			Category: "scale", State: "Active",
			VCpu: 32, Ram: 131072, Disk: 600, CpuType: "dedicated",
			PriceMonthly:    84900, // $849/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 51200,
			SortOrder:       80,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx53","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx53","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx53","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "mega", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Mega", Description: "Maximum single-node power. 48 dedicated vCPU, 192 GB RAM, 960 GB SSD.",
			Category: "mega", State: "Active",
			VCpu: 48, Ram: 196608, Disk: 960, CpuType: "dedicated",
			PriceMonthly:    129900, // $1,299/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 61440,
			SortOrder:       90,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"ccx63","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"ccx63","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"ccx63","location":"sin"}
			}`,
		},
		{
			Owner: owner, Name: "ultra", CreatedTime: now, UpdatedTime: now,
			DisplayName: "Ultra", Description: "Extreme compute cluster. 96 dedicated vCPU, 384 GB RAM, 1.9 TB SSD.",
			Category: "ultra", State: "Active",
			VCpu: 96, Ram: 393216, Disk: 1920, CpuType: "dedicated",
			PriceMonthly:    399900, // $3,999/mo
			Regions:         "us,eu,sg",
			TrafficIncluded: 122880,
			SortOrder:       100,
			ProviderMapping: `{
				"us":{"provider":"Hetzner","serverType":"2x-ccx63","location":"ash"},
				"eu":{"provider":"Hetzner","serverType":"2x-ccx63","location":"fsn1"},
				"sg":{"provider":"Hetzner","serverType":"2x-ccx63","location":"sin"}
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
