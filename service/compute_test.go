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

import (
	"math"
	"testing"

	"github.com/digitalocean/godo"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestHanzoPriceMarkup(t *testing.T) {
	// Standard sizes use the base markup; GPU sizes the gentler GPU markup.
	if got := HanzoPrice(48.0, false); !almost(got, 67.2) {
		t.Fatalf("base monthly markup: got %v want 67.2", got)
	}
	if got := HanzoPrice(0.00744, false); !almost(got, 0.01042) {
		t.Fatalf("base hourly markup: got %v want 0.01042", got)
	}
	if got := HanzoPrice(3.0, true); !almost(got, 3.75) {
		t.Fatalf("gpu markup: got %v want 3.75", got)
	}
	// Resale price must always exceed wholesale — the margin invariant.
	for _, do := range []float64{0.007, 0.5, 48, 3200} {
		if HanzoPrice(do, false) <= do || HanzoPrice(do, true) <= do {
			t.Fatalf("resale price must exceed wholesale for %v", do)
		}
	}
}

func TestIsGPUSize(t *testing.T) {
	cases := []struct {
		size godo.Size
		want bool
	}{
		{godo.Size{Slug: "s-2vcpu-4gb"}, false},
		{godo.Size{Slug: "gpu-h100x1-80gb"}, true},
		{godo.Size{Slug: "g-2vcpu-8gb", GPUInfo: &godo.GPUInfo{Count: 1, Model: "H100"}}, true},
	}
	for _, c := range cases {
		if got := isGPUSize(c.size); got != c.want {
			t.Fatalf("isGPUSize(%q)=%v want %v", c.size.Slug, got, c.want)
		}
	}
}

func TestSizeInfoFromDO_GPU(t *testing.T) {
	s := godo.Size{
		Slug:         "gpu-h100x1-80gb",
		Vcpus:        20,
		Memory:       240 * 1024,
		Disk:         720,
		Available:    true,
		Regions:      []string{"nyc2", "tor1"},
		PriceHourly:  3.0,
		PriceMonthly: 2000.0,
		GPUInfo:      &godo.GPUInfo{Count: 1, Model: "H100", VRAM: &godo.VRAM{Amount: 80, Unit: "GiB"}},
	}
	si := sizeInfoFromDO(s)
	if si.GPU == nil || si.GPU.Model != "H100" || si.GPU.Count != 1 || si.GPU.Vram != 80 {
		t.Fatalf("gpu spec not mapped: %+v", si.GPU)
	}
	if !almost(si.PriceHourly, HanzoPrice(3.0, true)) {
		t.Fatalf("gpu hourly resale: got %v", si.PriceHourly)
	}
	if !almost(si.PriceMonthly, HanzoPrice(2000.0, true)) {
		t.Fatalf("gpu monthly resale: got %v", si.PriceMonthly)
	}
}

// TestOrgIsolationPredicate proves the per-tenant isolation invariant: a droplet
// tagged for one org is visible ONLY to that org. This is the security boundary
// RED must verify end-to-end; here it is asserted at the data layer.
func TestOrgIsolationPredicate(t *testing.T) {
	if orgTag("maxpower") != "hanzo-org:maxpower" {
		t.Fatalf("orgTag format: got %q", orgTag("maxpower"))
	}
	d := &godo.Droplet{Tags: []string{"managed-by:hanzo-visor", "hanzo-org:maxpower"}}
	if !dropletHasOrgTag(d, "maxpower") {
		t.Fatal("owner org must see its own machine")
	}
	if dropletHasOrgTag(d, "hanzo") {
		t.Fatal("cross-tenant leak: another org must NOT see this machine")
	}
	if dropletHasOrgTag(&godo.Droplet{Tags: nil}, "maxpower") {
		t.Fatal("untagged droplet must not match any org")
	}
}
