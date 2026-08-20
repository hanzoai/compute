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
	"os"
	"testing"
)

// TestLiveDOCatalog exercises the real DigitalOcean catalog through the exact
// production code path (platform client -> godo -> mapping -> resale pricing).
// It is skipped unless DIGITALOCEAN_ACCESS_TOKEN is set, so it never runs in CI
// without credentials.
func TestLiveDOCatalog(t *testing.T) {
	if os.Getenv("DIGITALOCEAN_ACCESS_TOKEN") == "" {
		t.Skip("DIGITALOCEAN_ACCESS_TOKEN unset — skipping live DO catalog test")
	}

	regions, err := ListRegions()
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if len(regions) == 0 {
		t.Fatal("expected at least one region")
	}
	t.Logf("regions: %d (e.g. %s = %q)", len(regions), regions[0].Slug, regions[0].Name)

	sizes, err := ListSizes()
	if err != nil {
		t.Fatalf("ListSizes: %v", err)
	}
	if len(sizes) == 0 {
		t.Fatal("expected at least one size")
	}
	for _, s := range sizes {
		if s.PriceHourly <= 0 && s.PriceMonthly <= 0 {
			continue
		}
		t.Logf("sample size: %s vcpus=%d mem=%dMB $%.5f/hr $%.2f/mo", s.Slug, s.Vcpus, s.MemoryMB, s.PriceHourly, s.PriceMonthly)
		break
	}

	gpus, err := ListGPUSizes()
	if err != nil {
		t.Fatalf("ListGPUSizes: %v", err)
	}
	t.Logf("gpu sizes: %d", len(gpus))
	for _, g := range gpus {
		model := ""
		if g.GPU != nil {
			model = g.GPU.Model
		}
		t.Logf("gpu: %s model=%s $%.4f/hr $%.2f/mo", g.Slug, model, g.PriceHourly, g.PriceMonthly)
	}
}
