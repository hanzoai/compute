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

import (
	"testing"
)

// The price arithmetic, worked by hand: a running hour's cost is the
// instance's list price, its public IPv4 address and its gp3 root volume, and the
// price is that plus one third, rounded UP to the cent. A stopped hour is the
// volume alone.
func TestHourlyCentsArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listMicros int64
		diskGB     int64
		want       int64
	}{
		// $1.006/h + $0.005 IPv4 + 200 GB x $0.08/730h = $1.032918/h; x 4/3 = $1.377224 -> 138c.
		{"g5.xlarge", 1_006_000, 200, 138},
		// $0.0416/h + $0.005 + 50 GB = $0.052079/h; x 4/3 = $0.069439 -> 7c.
		{"t3.medium", 41_600, 50, 7},
		// $0.2016/h + $0.005 + 50 GB = $0.212079/h; x 4/3 = $0.282772 -> 29c (28c without the address).
		{"m7i.xlarge", 201_600, 50, 29},
		// Exactly 3 cents of cost is exactly 4 cents of price: no rounding up.
		{"exact", 25_000, 0, 4},
		// One micro-dollar over rounds up to the next cent.
		{"one over", 25_001, 0, 5},
		// No instance is no running hour.
		{"no instance", 0, 730, 0},
	} {
		if got := hourlyCents(tc.listMicros, tc.diskGB); got != tc.want {
			t.Errorf("%s: hourlyCents(%d, %d) = %d, want %d", tc.name, tc.listMicros, tc.diskGB, got, tc.want)
		}
	}
	for diskGB, want := range map[int64]int64{
		// 50 GB x $0.08/730h = $0.005479/h; x 4/3 = $0.007306 -> 1c.
		50: 1,
		// 200 GB: $0.021918/h; x 4/3 = $0.029224 -> 3c.
		200: 3,
		500: 8, 1000: 15,
		// 730 GB is exactly $0.08/h; x 4/3 = $0.106667 -> 11c.
		730: 11,
		0:   0,
	} {
		if got := stoppedCents(diskGB); got != want {
			t.Errorf("stoppedCents(%d) = %d, want %d", diskGB, got, want)
		}
	}
}

// Every size is priced at cost plus one third, never below it and never a whole
// cent above it, running and stopped.
func TestEverySizeIsPricedAtCostPlusTheFee(t *testing.T) {
	for _, o := range offers {
		// Both sides over one 730-hour month in micro-dollars, times three, so the
		// comparison is exact.
		for _, c := range []struct {
			what  string
			cost  int64
			cents int64
		}{
			{"running", (o.listMicros+ipv4MicrosPerHour)*hoursPerMonth + o.diskGB*gp3MicrosPerGBMonth, o.cents()},
			{"stopped", o.diskGB * gp3MicrosPerGBMonth, stoppedCents(o.diskGB)},
		} {
			price := c.cents * microsPerCent * hoursPerMonth
			if 3*price < 4*c.cost {
				t.Errorf("%s %s: %d cents is below cost plus the fee", o.slug, c.what, c.cents)
			}
			if 3*(price-microsPerCent*hoursPerMonth) >= 4*c.cost {
				t.Errorf("%s %s: %d cents is a whole cent above cost plus the fee", o.slug, c.what, c.cents)
			}
		}
	}
}

// The catalog as sold. A change here changes what customers pay, so it is a
// change to this table and nowhere else.
func TestTheCatalogAsSold(t *testing.T) {
	want := map[string]struct {
		cents int64
		gpus  int
		model string
	}{
		"t3.medium":    {7, 0, ""},
		"m7i.large":    {15, 0, ""},
		"m7i.xlarge":   {29, 0, ""},
		"m7i.2xlarge":  {56, 0, ""},
		"c7i.xlarge":   {26, 0, ""},
		"r7i.xlarge":   {37, 0, ""},
		"g6.xlarge":    {111, 1, "L4"},
		"g6.12xlarge":  {622, 4, "L4"},
		"g5.xlarge":    {138, 1, "A10G"},
		"g5.12xlarge":  {765, 4, "A10G"},
		"g5.48xlarge":  {2188, 8, "A10G"},
		"g6e.xlarge":   {252, 1, "L40S"},
		"p4d.24xlarge": {2943, 8, "A100"},
		"p5.48xlarge":  {7354, 8, "H100"},
	}
	if len(offers) != len(want) {
		t.Fatalf("the catalog has %d sizes, want %d", len(offers), len(want))
	}
	seen := map[string]bool{}
	for _, o := range offers {
		w, ok := want[o.slug]
		if !ok {
			t.Errorf("%s is for sale and not in this table", o.slug)
			continue
		}
		if seen[o.slug] {
			t.Errorf("%s is listed twice", o.slug)
		}
		seen[o.slug] = true
		if o.cents() != w.cents {
			t.Errorf("%s = %d cents/h, want %d", o.slug, o.cents(), w.cents)
		}
		switch {
		case w.gpus == 0 && o.gpu != nil:
			t.Errorf("%s carries a GPU it does not have", o.slug)
		case w.gpus > 0 && (o.gpu == nil || o.gpu.Count != w.gpus || o.gpu.Model != w.model):
			t.Errorf("%s gpu = %+v, want %d x %s", o.slug, o.gpu, w.gpus, w.model)
		}
		if o.vcpus <= 0 || o.memoryMB <= 0 || o.diskGB <= 0 || o.listMicros <= 0 {
			t.Errorf("%s has an unstated shape or cost: %+v", o.slug, o)
		}
		if o.burstable != (o.slug[0] == 't') {
			t.Errorf("%s: only a T-family type is burstable", o.slug)
		}
	}
	if _, ok := offerFor(DefaultLaunchSize); !ok {
		t.Fatalf("the default size %s is not for sale", DefaultLaunchSize)
	}
}

// What a caller is shown is what the meter debits: the dollar figures are the
// cents, and a size is available only where the hosted account is configured.
func TestASizeShowsWhatItDebits(t *testing.T) {
	t.Setenv(keyRegion, "")
	si := SizeBySlug("g5.xlarge")
	if si == nil {
		t.Fatal("g5.xlarge is for sale")
	}
	if si.CentsHourly != 138 || si.PriceHourly != 1.38 || si.PriceMonthly != 1007.40 {
		t.Fatalf("g5.xlarge shows %d / %v / %v, want 138 / 1.38 / 1007.40", si.CentsHourly, si.PriceHourly, si.PriceMonthly)
	}
	if si.Available || len(si.Regions) != 0 {
		t.Fatalf("with no region configured nothing is available: %+v", si)
	}
	if cents, err := HourlyCents("g5.xlarge"); err != nil || cents != si.CentsHourly {
		t.Fatalf("the meter prices g5.xlarge at %d (%v), the catalog shows %d", cents, err, si.CentsHourly)
	}
	if len(ListRegions()) != 0 {
		t.Fatal("with no region configured there is no region")
	}

	t.Setenv(keyRegion, "us-east-1")
	si = SizeBySlug("g5.xlarge")
	if !si.Available || len(si.Regions) != 1 || si.Regions[0] != "us-east-1" {
		t.Fatalf("configured, g5.xlarge = %+v", si)
	}
	regions := ListRegions()
	if len(regions) != 1 || regions[0].Slug != "us-east-1" || len(regions[0].Sizes) != len(offers) {
		t.Fatalf("regions = %+v", regions)
	}
	gpus := ListGPUSizes()
	for _, g := range gpus {
		if g.GPU == nil {
			t.Fatalf("%s is listed as a GPU size with no GPU", g.Slug)
		}
	}
	if len(gpus) != 8 || len(ListSizes()) != len(offers) {
		t.Fatalf("gpus = %d, sizes = %d", len(gpus), len(ListSizes()))
	}
	if SizeBySlug("s-2vcpu-4gb") != nil {
		t.Fatal("a size that is not for sale resolved")
	}
}
