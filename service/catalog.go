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

// catalog.go is what hosted compute sells: every instance type, its shape, the
// root volume it launches with and what it costs Hanzo. It is the one table the
// catalog routes, the launch quote, the launch gate and the hourly meter all read,
// so a size is priced one way everywhere. A type that is not in it cannot be
// launched and cannot be billed.

// GPUSpec is the accelerator detail of a GPU size.
type GPUSpec struct {
	Count    int    `json:"count"`
	Model    string `json:"model"`
	Vram     int    `json:"vram"`
	VramUnit string `json:"vramUnit"`
}

// SizeInfo is one size as a caller sees it. Only Hanzo's price is exposed; what
// it costs Hanzo stays in the catalog.
type SizeInfo struct {
	Slug      string   `json:"slug"`
	Vcpus     int      `json:"vcpus"`
	MemoryMB  int      `json:"memoryMb"`
	DiskGB    int      `json:"diskGb"`
	Available bool     `json:"available"`
	Regions   []string `json:"regions"`
	GPU       *GPUSpec `json:"gpu,omitempty"`
	Currency  string   `json:"currency"`
	// CentsHourly is the price per running hour in whole cents, exactly what the
	// launch and the hourly meter debit.
	CentsHourly int64 `json:"centsHourly"`
	// CentsStopped is the price per stopped hour in whole cents: the root volume,
	// which the hourly meter debits while the machine is stopped.
	CentsStopped int64 `json:"centsStopped"`
	// PriceHourly and PriceMonthly are CentsHourly in dollars, per hour and per
	// 730-hour month.
	PriceHourly  float64 `json:"priceHourly"`
	PriceMonthly float64 `json:"priceMonthly"`
}

// RegionInfo is one region a machine can launch in.
type RegionInfo struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Features  []string `json:"features"`
	Sizes     []string `json:"sizes"`
}

// offer is one instance type for sale.
type offer struct {
	slug     string // the EC2 instance type
	vcpus    int
	memoryMB int
	gpu      *GPUSpec
	// diskGB is the gp3 root volume every launch of this size gets, and it is
	// priced in.
	diskGB int64
	// listMicros is AWS's on-demand Linux list price for the type in us-east-1,
	// in micro-dollars per hour.
	listMicros int64
	// burstable marks a T-family type, launched with standard CPU credits so it
	// cannot accrue surplus-credit charges the price does not cover.
	burstable bool
}

// cents is the offer's price per running hour.
func (o offer) cents() int64 { return hourlyCents(o.listMicros, o.diskGB) }

func gpu(count int, model string, vramGiB int) *GPUSpec {
	return &GPUSpec{Count: count, Model: model, Vram: vramGiB, VramUnit: "GiB"}
}

// offers is the catalog. List prices are AWS's published us-east-1 on-demand
// Linux rates (AWS price list of 2026-09-21); memory is in MiB.
var offers = []offer{
	{slug: "t3.medium", vcpus: 2, memoryMB: 4 << 10, diskGB: 50, listMicros: 41_600, burstable: true},
	{slug: "m7i.large", vcpus: 2, memoryMB: 8 << 10, diskGB: 50, listMicros: 100_800},
	{slug: "m7i.xlarge", vcpus: 4, memoryMB: 16 << 10, diskGB: 50, listMicros: 201_600},
	{slug: "m7i.2xlarge", vcpus: 8, memoryMB: 32 << 10, diskGB: 50, listMicros: 403_200},
	{slug: "c7i.xlarge", vcpus: 4, memoryMB: 8 << 10, diskGB: 50, listMicros: 178_500},
	{slug: "r7i.xlarge", vcpus: 4, memoryMB: 32 << 10, diskGB: 50, listMicros: 264_600},

	{slug: "g6.xlarge", vcpus: 4, memoryMB: 16 << 10, gpu: gpu(1, "L4", 24), diskGB: 200, listMicros: 804_800},
	{slug: "g6.12xlarge", vcpus: 48, memoryMB: 192 << 10, gpu: gpu(4, "L4", 24), diskGB: 500, listMicros: 4_601_600},
	{slug: "g5.xlarge", vcpus: 4, memoryMB: 16 << 10, gpu: gpu(1, "A10G", 24), diskGB: 200, listMicros: 1_006_000},
	{slug: "g5.12xlarge", vcpus: 48, memoryMB: 192 << 10, gpu: gpu(4, "A10G", 24), diskGB: 500, listMicros: 5_672_000},
	{slug: "g5.48xlarge", vcpus: 192, memoryMB: 768 << 10, gpu: gpu(8, "A10G", 24), diskGB: 1000, listMicros: 16_288_000},
	{slug: "g6e.xlarge", vcpus: 4, memoryMB: 32 << 10, gpu: gpu(1, "L40S", 48), diskGB: 200, listMicros: 1_861_000},
	{slug: "p4d.24xlarge", vcpus: 96, memoryMB: 1152 << 10, gpu: gpu(8, "A100", 40), diskGB: 1000, listMicros: 21_957_642},
	{slug: "p5.48xlarge", vcpus: 192, memoryMB: 2048 << 10, gpu: gpu(8, "H100", 80), diskGB: 1000, listMicros: 55_040_000},
}

// DefaultLaunchSize is the size a launch gets when the caller names none — the
// tabs "New cloud machine" button, a bare CLI launch. A shell and `hanzo link`
// run comfortably on it.
const DefaultLaunchSize = "t3.medium"

// offerFor returns the catalog entry for an instance type.
func offerFor(slug string) (offer, bool) {
	for _, o := range offers {
		if o.slug == slug {
			return o, true
		}
	}
	return offer{}, false
}

// info is the offer as a caller sees it in region; an empty region means the
// hosted account is not configured, so nothing is available.
func (o offer) info(region string) SizeInfo {
	cents := o.cents()
	si := SizeInfo{
		Slug:         o.slug,
		Vcpus:        o.vcpus,
		MemoryMB:     o.memoryMB,
		DiskGB:       int(o.diskGB),
		Available:    region != "",
		Regions:      []string{},
		Currency:     "USD",
		CentsHourly:  cents,
		CentsStopped: stoppedCents(o.diskGB),
		PriceHourly:  float64(cents) / 100,
		PriceMonthly: float64(cents*hoursPerMonth) / 100,
	}
	if region != "" {
		si.Regions = []string{region}
	}
	if o.gpu != nil {
		g := *o.gpu
		si.GPU = &g
	}
	return si
}

// ListSizes returns every size for sale.
func ListSizes() []SizeInfo {
	region := hostedConfig().Region
	out := make([]SizeInfo, 0, len(offers))
	for _, o := range offers {
		out = append(out, o.info(region))
	}
	return out
}

// ListGPUSizes returns the sizes that carry accelerators.
func ListGPUSizes() []SizeInfo {
	region := hostedConfig().Region
	out := make([]SizeInfo, 0, len(offers))
	for _, o := range offers {
		if o.gpu != nil {
			out = append(out, o.info(region))
		}
	}
	return out
}

// ListRegions returns the region hosted machines launch in: the configured one,
// or none.
func ListRegions() []RegionInfo {
	region := hostedConfig().Region
	if region == "" {
		return []RegionInfo{}
	}
	sizes := make([]string, 0, len(offers))
	features := []string{}
	for _, o := range offers {
		sizes = append(sizes, o.slug)
		if o.gpu != nil && len(features) == 0 {
			features = append(features, "gpu")
		}
	}
	return []RegionInfo{{Slug: region, Name: region, Available: true, Features: features, Sizes: sizes}}
}

// SizeBySlug returns the size for a slug, or nil when it is not for sale.
func SizeBySlug(slug string) *SizeInfo {
	o, ok := offerFor(slug)
	if !ok {
		return nil
	}
	si := o.info(hostedConfig().Region)
	return &si
}
