// Copyright 2025 The casbin Authors. All Rights Reserved.
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

package billing

// dropletPricing maps DO size slugs to their hourly cost in cents.
// Source: DigitalOcean pricing page. Can be updated from DO API later.
var dropletPricing = map[string]int64{
	// Standard droplets
	"s-1vcpu-512mb-10gb": 1,
	"s-1vcpu-1gb":        1,
	"s-1vcpu-2gb":        2,
	"s-2vcpu-2gb":        3,
	"s-2vcpu-4gb":        4,
	"s-4vcpu-8gb":        7,
	"s-8vcpu-16gb":       14,
	"s-16vcpu-32gb":      29,
	"s-20vcpu-96gb":      71,
	"s-24vcpu-128gb":     95,
	"s-32vcpu-64gb":      57,

	// General purpose
	"g-2vcpu-8gb":   7,
	"g-4vcpu-16gb":  14,
	"g-8vcpu-32gb":  27,
	"g-16vcpu-64gb": 54,

	// General purpose + SSD
	"gd-2vcpu-8gb":   8,
	"gd-4vcpu-16gb":  15,
	"gd-8vcpu-32gb":  30,
	"gd-16vcpu-64gb": 60,

	// Memory-optimized
	"m-2vcpu-16gb":   13,
	"m-4vcpu-32gb":   25,
	"m-8vcpu-64gb":   50,
	"m-16vcpu-128gb": 100,

	// Storage-optimized
	"so-2vcpu-16gb":   13,
	"so-4vcpu-32gb":   25,
	"so-8vcpu-64gb":   50,
	"so-16vcpu-128gb": 100,

	// CPU-optimized
	"c-2vcpu-4gb":   6,
	"c-4vcpu-8gb":   13,
	"c-8vcpu-16gb":  25,
	"c-16vcpu-32gb": 50,
	"c-32vcpu-64gb": 100,
}

// GetHourlyCostCents returns the hourly cost in cents for a given DO size slug.
// Returns 0 if the size is not found.
func GetHourlyCostCents(sizeSlug string) int64 {
	if cost, ok := dropletPricing[sizeSlug]; ok {
		return cost
	}
	return 0
}
